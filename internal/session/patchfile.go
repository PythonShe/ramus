package session

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"ramus/internal/git/run"
	"ramus/internal/git/write"
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
// or not at all, and calls done on the main thread.
func (s *Session) SavePatch(req PatchSave, file string, done func(error)) {
	r := s.Repo
	if r == nil {
		return
	}
	update := s.update
	r.Life().Go(func(ctx context.Context) {
		err := savePatch(ctx, r.Runner, r.GitDir, r.WorkTree, req, file)
		if ctx.Err() == nil {
			update(func() { done(err) })
		}
	})
}

func savePatch(ctx context.Context, rn *run.Runner, gitDir, workTree string, req PatchSave, file string) (err error) {
	if len(req.Commits) > write.MaxPick {
		return write.ErrTooMany
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), ".ramus-patch-*")
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
		for i, c := range req.Commits {
			_, err = rn.Run(ctx, &run.Cmd{GitDir: gitDir, Read: true, Stdout: tmp,
				Args: []string{"format-patch", "--stdout", "-1", "--start-number=" + strconv.Itoa(i+1), "--end-of-options", c}})
			if err != nil {
				return err
			}
		}
	case len(req.Paths) > 0:
		args := []string{"diff", "--binary", "--no-color", "--no-ext-diff"}
		if req.Cached {
			args = append(args, "--cached")
		}
		_, err = rn.Run(ctx, &run.Cmd{GitDir: gitDir, WorkTree: workTree, Read: true, Stdout: tmp,
			Env: []string{"GIT_LITERAL_PATHSPECS=1"}, Args: append(append(args, "--"), req.Paths...)})
		if err != nil {
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

// PatchFile is what a patch file holds, read before it is applied.
type PatchFile struct {
	Path string
	// Mailbox is a patch git format-patch wrote, applied as commits.
	Mailbox bool
	// Files are the paths it changes.
	Files []string
}

// ReadPatchFile reads what a patch file changes off the main thread and
// hands it to done on it; git refuses a file that is not a patch.
func (s *Session) ReadPatchFile(file string, done func(PatchFile, error)) {
	r := s.Repo
	if r == nil {
		return
	}
	update := s.update
	r.Life().Go(func(ctx context.Context) {
		pf, err := readPatchFile(ctx, r.Runner, r.GitDir, r.WorkTree, file)
		update(func() { done(pf, err) })
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
