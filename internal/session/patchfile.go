package session

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"ramus/internal/git/pathsafe"
	"ramus/internal/git/run"
	"ramus/internal/git/write"
	"ramus/locales"
)

// PatchSave is what a patch file is made of: commits (hex, oldest first),
// each as a mail git am takes, or else the changes to Paths, staged ones
// when Cached is set.
type PatchSave struct {
	Commits []string
	Paths   []string
	Cached  bool
}

// SavePatch writes a patch to file off the main thread, replacing it whole
// or not at all, and calls done on the main thread: with ErrReopened when
// the repository closing, as Trust opens it again, cut it short.
func (s *Session) SavePatch(req PatchSave, file string, done func(error)) {
	r := s.Repo
	if r == nil {
		done(errNotOpen)
		return
	}
	r.Life().Go(func(ctx context.Context) {
		err := savePatch(ctx, r.Runner, r.GitDir, r.WorkTree, req, file)
		cut := ctx.Err() != nil
		// The file written, or not, is told whichever repository is open
		// now: it holds nothing of r.
		s.handOver(r, func() { done(err) }, func() {
			if cut {
				err = ErrReopened
			}
			done(err)
		})
	})
}

func savePatch(ctx context.Context, rn *run.Runner, gitDir, workTree string, req PatchSave, file string) (err error) {
	if len(req.Commits) > write.MaxPick {
		return write.ErrTooMany
	}
	tmp, err := createBeside(file)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	switch {
	case len(req.Commits) > 0:
		// format-patch skips a merge: -1 on one walks past it and writes
		// the next commit that is not, under the merge's name.
		var res run.Result
		res, err = rn.Run(ctx, &run.Cmd{GitDir: gitDir, Read: true,
			Args: append(append([]string{"rev-list", "--no-walk", "--min-parents=2", "--end-of-options"}, req.Commits...), "--")})
		if err != nil {
			return err
		}
		if len(bytes.TrimSpace(res.Stdout)) > 0 {
			return &sayError{locales.SessionMergeAsPatch}
		}
		for i, c := range req.Commits {
			_, err = rn.Run(ctx, &run.Cmd{GitDir: gitDir, Read: true, Stdout: tmp,
				Args: []string{"format-patch", "--stdout", "-1", "--start-number=" + strconv.Itoa(i+1), "--end-of-options", c, "--"}})
			if err != nil {
				return err
			}
		}
	case len(req.Paths) > 0:
		if err = diffPatch(ctx, rn, gitDir, workTree, req, tmp); err != nil {
			return err
		}
	default:
		return errors.New("session: nothing to save as a patch")
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file)
}

// createBeside makes a new file in file's folder, to be renamed over it,
// with the mode os.Create gives (0666 less the umask) rather than
// os.CreateTemp's 0600, as a patch is made to be shared; or, replacing a
// file, that file's mode.
func createBeside(file string) (*os.File, error) {
	mode, keep := os.FileMode(0o666), false
	if fi, err := os.Stat(file); err == nil && fi.Mode().IsRegular() {
		mode, keep = fi.Mode().Perm(), true
	}
	for try := 0; ; try++ {
		var b [8]byte
		rand.Read(b[:])
		f, err := os.OpenFile(filepath.Join(filepath.Dir(file), ".ramus-patch-"+hex.EncodeToString(b[:])),
			os.O_RDWR|os.O_CREATE|os.O_EXCL, mode)
		if errors.Is(err, fs.ErrExist) && try < 100 {
			continue
		}
		if err == nil && keep {
			// The umask applied to the new file; the old one's mode is
			// what the user had.
			if err = f.Chmod(mode); err != nil {
				f.Close()
				os.Remove(f.Name())
			}
		}
		return f, err
	}
}

// diffPatch writes the changes to req.Paths as a patch git apply takes
// on a checkout of the same base, whatever the user's diff config says:
// prefixes (diff.noprefix, mnemonicPrefix, srcPrefix), diff.relative, a
// textconv driver's text in place of the file's, a submodule's log. The
// untracked files among the paths, which git diff leaves out, are in it
// as new files.
func diffPatch(ctx context.Context, rn *run.Runner, gitDir, workTree string, req PatchSave, out io.Writer) error {
	args := []string{"diff", "--binary", "--no-color", "--no-ext-diff", "--no-textconv", "--no-relative",
		"--src-prefix=a/", "--dst-prefix=b/", "--submodule=short"}
	env := []string{"GIT_LITERAL_PATHSPECS=1"}
	if req.Cached {
		args = append(args, "--cached")
	} else {
		index, err := intendToAdd(ctx, rn, gitDir, workTree, req.Paths)
		if err != nil {
			return err
		}
		if index != "" {
			defer os.Remove(index)
			env = append(env, "GIT_INDEX_FILE="+index)
			// Otherwise git shows a file added with -N as an empty file
			// changed, which does not apply where the file is not.
			args = append(args, "--ita-invisible-in-index")
		}
	}
	// git diff reads paths only as arguments: many take several runs,
	// one patch after another.
	for _, c := range write.SplitArgs(run.Cmd{GitDir: gitDir, WorkTree: workTree, Read: true, Stdout: out, Env: env,
		Config: []string{"diff.suppressBlankEmpty=false"}, Args: append(args, "--")}, req.Paths) {
		if _, err := rn.Run(ctx, &c); err != nil {
			return err
		}
	}
	return nil
}

// copyIndex copies the index of gitDir into f, which it closes, keeping
// the index's time; copied is false when there is no index.
func copyIndex(gitDir string, f *os.File) (copied bool, err error) {
	// As every file under the git directory is opened: a named pipe there
	// would hold the open for ever.
	src, err := pathsafe.OpenRegular(filepath.Join(gitDir, "index"))
	if errors.Is(err, os.ErrNotExist) {
		return false, f.Close()
	}
	if err != nil {
		f.Close()
		return false, err
	}
	_, err = io.Copy(f, src)
	st, statErr := src.Stat()
	src.Close()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	// git trusts an entry's stat data only when the file is older than
	// the index; the copy keeps the index's time, so files are not all
	// hashed again.
	if err == nil && statErr == nil {
		err = os.Chtimes(f.Name(), st.ModTime(), st.ModTime())
	}
	return err == nil, err
}

// intendToAdd returns a copy of the index in which the untracked files
// among paths are added with git add -N, so that git diff shows them; ""
// when none is untracked. The repository's own index is left as it is.
func intendToAdd(ctx context.Context, rn *run.Runner, gitDir, workTree string, paths []string) (index string, err error) {
	var untracked []string
	for _, c := range write.SplitArgs(run.Cmd{GitDir: gitDir, WorkTree: workTree, Read: true, Env: []string{"GIT_LITERAL_PATHSPECS=1"},
		Args: []string{"ls-files", "-z", "--others", "--exclude-standard", "--"}}, paths) {
		res, err := rn.Run(ctx, &c)
		if err != nil {
			return "", err
		}
		for p := range bytes.SplitSeq(res.Stdout, []byte{0}) {
			if len(p) > 0 {
				untracked = append(untracked, string(p))
			}
		}
	}
	if len(untracked) == 0 {
		return "", nil
	}
	f, err := os.CreateTemp("", "ramus-patch-*.index")
	if err != nil {
		return "", err
	}
	index = f.Name()
	defer func() {
		if err != nil {
			os.Remove(index)
			index = ""
		}
		os.Remove(index + ".lock")
	}()
	copied, err := copyIndex(gitDir, f)
	if err != nil {
		return "", err
	}
	if !copied {
		// No index yet: git starts from an empty one.
		os.Remove(index)
	}
	// It writes the copy only, so it counts as a read: the repository
	// is not changed, trusted or not.
	// A split index would have git write a shared index into the git
	// directory.
	for _, c := range write.SplitArgs(run.Cmd{GitDir: gitDir, WorkTree: workTree, Read: true,
		Env:    []string{"GIT_LITERAL_PATHSPECS=1", "GIT_INDEX_FILE=" + index},
		Config: []string{"core.splitIndex=false"},
		Args:   []string{"add", "--intent-to-add", "--"}}, untracked) {
		if _, err = rn.Run(ctx, &c); err != nil {
			return "", err
		}
	}
	return index, nil
}

// PatchFile is what a patch file holds, read before it is applied.
type PatchFile struct {
	Path string
	// Mailbox is a patch git format-patch wrote, applied as commits.
	Mailbox bool
	// Files are the paths it changes.
	Files []string
}

// ReadPatchFile reads what a patch file changes off the main thread and
// hands it to done on it; git refuses a file that is not a patch. A read
// whose repository closed and opened again (Trust) is answered
// ErrReopened: what to apply is asked of the repository as it is now.
func (s *Session) ReadPatchFile(file string, done func(PatchFile, error)) {
	r := s.Repo
	if r == nil {
		done(PatchFile{Path: file}, errNotOpen)
		return
	}
	r.Life().Go(func(ctx context.Context) {
		pf, err := readPatchFile(ctx, r.Runner, r.GitDir, r.WorkTree, file)
		s.handOver(r, func() { done(pf, err) }, func() { done(PatchFile{Path: file}, ErrReopened) })
	})
}

func readPatchFile(ctx context.Context, rn *run.Runner, gitDir, workTree, file string) (PatchFile, error) {
	pf := PatchFile{Path: file}
	f, err := os.Open(file)
	if err != nil {
		return pf, err
	}
	head := make([]byte, 128)
	n, _ := io.ReadFull(f, head)
	f.Close()
	head = head[:n]
	// git format-patch starts each mail with "From <commit> Mon Sep 17
	// 00:00:00 2001", a fixed date.
	pf.Mailbox = bytes.HasPrefix(head, []byte("From ")) && bytes.Contains(head, []byte("Mon Sep 17 00:00:00 2001"))
	res, err := rn.Run(ctx, &run.Cmd{GitDir: gitDir, WorkTree: workTree, Read: true, MaxOutput: 16 << 20,
		Args: []string{"apply", "--numstat", "-z", "--", file}})
	if err != nil {
		return pf, err
	}
	pf.Files = parseNumstatZ(res.Stdout)
	return pf, nil
}

// parseNumstatZ reads git apply --numstat -z: "added\tdeleted\tpath\0",
// or for a rename "added\tdeleted\t\0old\0new\0". Each path is listed
// once.
func parseNumstatZ(out []byte) []string {
	var paths []string
	seen := map[string]bool{}
	add := func(p []byte) {
		if !seen[string(p)] {
			seen[string(p)] = true
			paths = append(paths, string(p))
		}
	}
	fields := bytes.Split(out, []byte{0})
	for i := 0; i < len(fields); i++ {
		rec := fields[i]
		if len(rec) == 0 {
			continue
		}
		parts := bytes.SplitN(rec, []byte{'\t'}, 3)
		if len(parts) != 3 {
			continue
		}
		if len(parts[2]) > 0 {
			add(parts[2])
			continue
		}
		if i+2 < len(fields) {
			add(fields[i+1])
			add(fields[i+2])
			i += 2
		}
	}
	return paths
}
