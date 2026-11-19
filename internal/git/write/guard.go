package write

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ramus/internal/git/pathsafe"
	"ramus/internal/git/run"
)

// Expect is the state a destructive action was decided on. The action
// refuses to run if any of it changed in the meantime, since the user
// confirmed losing what they saw, not what is there now.
type Expect struct {
	// Branch is the branch HEAD named ("refs/heads/main"), "" when
	// detached: a switch to another branch at the same commit is a change.
	Branch string
	// Head is the commit HEAD was on (hex), "" for unborn.
	Head string
	// Refs maps each ref the action moves or deletes to the value seen,
	// "" for a ref that did not exist.
	Refs map[string]string
	// Files are the work tree files at stake as they were seen.
	Files []FileState
	// AnyHead leaves Branch and Head unchecked, for an action that does
	// not depend on where HEAD is, as a tag of a commit chosen in history.
	// AnyCommit checks Branch alone, for an action on the branch as it
	// is, as a push of it.
	AnyHead, AnyCommit bool
	// Replay, when set, is a rewrite of a commit through a rebase of
	// HEAD's branch from the commit's parent. The action refuses unless
	// the commit is in HEAD's history, as the rebase would otherwise
	// replay the branch onto that parent, and when a merge is among the
	// commits replayed, as replaying it asks for its conflicts again.
	Replay *Rewrite
}

// ErrNotInHistory is a rewrite of a commit the current branch does not
// have.
var ErrNotInHistory = errors.New("write: the commit is not in the current branch's history; switch to a branch that has it first")

func checkReplay(ctx context.Context, r *run.Runner, gitDir string, rw *Rewrite) error {
	_, err := r.Run(ctx, &run.Cmd{GitDir: gitDir, Read: true, Args: []string{"merge-base", "--is-ancestor", rw.Commit, "HEAD"}})
	var e *run.Error
	if errors.As(err, &e) && e.ExitCode == 1 {
		return ErrNotInHistory
	}
	if err != nil {
		return err
	}
	args := []string{"rev-list", "--merges", "-n1", "HEAD"}
	if rw.Parent != "" {
		args = append(args, "^"+rw.Parent)
	}
	res, err := r.Run(ctx, &run.Cmd{GitDir: gitDir, Read: true, Args: append(args, "--")})
	if err != nil {
		return err
	}
	if merge := strings.TrimSpace(string(res.Stdout)); merge != "" {
		return fmt.Errorf("%w: merge %s comes after %s, and replaying it would ask for its conflicts again", ErrNeedsNewerGit, short(merge), short(rw.Commit))
	}
	return nil
}

func checkHead(ctx context.Context, r *run.Runner, gitDir string, x *Expect) error {
	branch, err := symbolic(ctx, r, gitDir)
	if err != nil {
		return err
	}
	if branch != x.Branch {
		return &StaleError{What: "the current branch", Saw: orDetached(x.Branch), Now: orDetached(branch)}
	}
	if x.AnyCommit {
		return nil
	}
	head, err := resolve(ctx, r, gitDir, "HEAD")
	if err != nil {
		return err
	}
	if head != x.Head {
		return &StaleError{What: "HEAD", Saw: short(x.Head), Now: short(head)}
	}
	return nil
}

// FileState is what was seen of a file: enough to tell that it changed
// without reading it.
type FileState struct {
	Path    string // relative to the work tree, slash-separated
	Exists  bool
	Size    int64
	Mode    os.FileMode
	ModTime time.Time
	// Ignored is a file or folder git ignores inside a folder observed,
	// which the write leaves alone: present, its contents neither
	// recorded nor checked.
	Ignored bool
	// Sum, when set, is ContentSum of the file's bytes as the user saw
	// them: the file is then judged by its contents, not its time, which
	// an edit of the same size within one tick of the clock leaves.
	Sum string
}

// ContentSum is the sum of data a FileState's Sum holds.
func ContentSum(data []byte) string {
	s := sha256.Sum256(data)
	return string(s[:])
}

// MaxObserved bounds the files Observe records.
const MaxObserved = 200000

// ErrTooManyFiles is an Observe of folders holding more than MaxObserved
// files.
var ErrTooManyFiles = errors.New("write: too many files to check before the action")

// Observe records paths as they are now, for an Expect. A folder is
// recorded file by file, since editing a file in place leaves the
// folder's own times alone.
func Observe(workTree string, paths []string) ([]FileState, error) {
	return observeExcept(workTree, paths, nil)
}

// ObserveIgnoring is Observe for a write that leaves what git ignores
// alone, as a discard, or a delete without ignored files, does: inside
// the folders it records, what git ignores, a dependency folder the
// repository hides as node_modules among them, is recorded as Ignored,
// not walked file by file. r runs git with the repository's settings,
// the folders it hides included; nil is Observe.
func ObserveIgnoring(ctx context.Context, r *run.Runner, w Where, paths []string) ([]FileState, error) {
	var folders []string
	for _, p := range paths {
		if fs := stat(w.WorkTree, p); fs.Exists && fs.Mode.IsDir() {
			folders = append(folders, p)
		}
	}
	if r == nil || len(folders) == 0 {
		return Observe(w.WorkTree, paths)
	}
	ignored := map[string]bool{}
	// ls-files takes no pathspec file: as many runs as the command line
	// needs.
	for _, c := range SplitArgs(run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Read: true, Env: []string{"GIT_LITERAL_PATHSPECS=1"},
		Args: []string{"ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory", "--full-name", "--"}}, folders) {
		res, err := r.Run(ctx, &c)
		if err != nil {
			return nil, err
		}
		for _, p := range strings.Split(string(res.Stdout), "\x00") {
			if p != "" {
				ignored[p] = true
			}
		}
	}
	return observeExcept(w.WorkTree, paths, ignored)
}

// observeExcept is Observe; ignored are the paths git ignores, a folder's
// with its slash, which it records as Ignored without walking them.
func observeExcept(workTree string, paths []string, ignored map[string]bool) ([]FileState, error) {
	var out []FileState
	for _, p := range paths {
		fs := stat(workTree, p)
		if !fs.Exists || !fs.Mode.IsDir() {
			out = append(out, fs)
			continue
		}
		root := filepath.Join(workTree, filepath.FromSlash(p))
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && d.Name() == ".git" {
				return filepath.SkipDir
			}
			// A repository inside, a submodule's work tree among them,
			// whose .git is a file: git leaves its files alone.
			if d.IsDir() && path != root {
				if _, err := os.Lstat(filepath.Join(path, ".git")); err == nil {
					return filepath.SkipDir
				}
			}
			rel, _ := filepath.Rel(workTree, path)
			fs := stat(workTree, filepath.ToSlash(rel))
			if path != root && (d.IsDir() && ignored[fs.Path+"/"] || !d.IsDir() && ignored[fs.Path]) {
				fs.Ignored = true
				out = append(out, fs)
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			out = append(out, fs)
			if len(out) > MaxObserved {
				return ErrTooManyFiles
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func stat(root, p string) FileState {
	fs := FileState{Path: p}
	if st, err := os.Lstat(filepath.Join(root, filepath.FromSlash(p))); err == nil {
		fs.Exists, fs.Size, fs.Mode, fs.ModTime = true, st.Size(), st.Mode(), st.ModTime()
	}
	return fs
}

func (a FileState) same(b FileState) bool {
	return a.Exists == b.Exists && a.Size == b.Size && a.Mode == b.Mode && a.ModTime.Equal(b.ModTime)
}

// StaleError is an action refused because what it was decided on changed.
type StaleError struct {
	What     string // "HEAD", a ref name, or a path
	Saw, Now string
}

func (e *StaleError) Error() string {
	return fmt.Sprintf("write: %s changed since it was shown (%s, now %s); look again before going on", e.What, e.Saw, e.Now)
}

func check(ctx context.Context, r *run.Runner, gitDir, workTree string, x *Expect) error {
	if !x.AnyHead {
		if err := checkHead(ctx, r, gitDir, x); err != nil {
			return err
		}
	}
	if x.Replay != nil {
		if err := checkReplay(ctx, r, gitDir, x.Replay); err != nil {
			return err
		}
	}
	for name, saw := range x.Refs {
		now, err := resolve(ctx, r, gitDir, name)
		if err != nil {
			return err
		}
		if now != saw {
			return &StaleError{What: name, Saw: short(saw), Now: short(now)}
		}
	}
	return checkFiles(workTree, x.Files)
}

// checkFiles refuses when a file seen changed, or one was added to a
// folder seen.
func checkFiles(workTree string, files []FileState) error {
	seen := make(map[string]bool, len(files))
	for _, f := range files {
		seen[f.Path] = true
	}
	for _, f := range files {
		if f.Ignored {
			continue
		}
		now := stat(workTree, f.Path)
		ok := now.same(f)
		if f.Sum != "" && f.Exists && f.Mode.IsRegular() {
			ok = now.Exists && now.Mode == f.Mode && now.Size == f.Size
			if ok {
				b, err := pathsafe.ReadFile(filepath.Join(workTree, filepath.FromSlash(f.Path)), f.Size)
				ok = err == nil && ContentSum(b) == f.Sum
			}
		}
		if !ok {
			return &StaleError{What: f.Path, Saw: describe(f), Now: describe(now)}
		}
		if !f.Exists || !f.Mode.IsDir() {
			continue
		}
		// A file added to a folder seen is found by listing it: the
		// folder's own time is not always moved by it, as on Windows.
		es, err := os.ReadDir(filepath.Join(workTree, filepath.FromSlash(f.Path)))
		if err != nil {
			return err
		}
		for _, e := range es {
			p := path.Join(f.Path, e.Name())
			if seen[p] || e.IsDir() && e.Name() == ".git" {
				continue
			}
			// A repository inside, which Observe leaves out as git leaves
			// its files alone.
			if e.IsDir() {
				if _, err := os.Lstat(filepath.Join(workTree, filepath.FromSlash(p), ".git")); err == nil {
					continue
				}
			}
			return &StaleError{What: p, Saw: describe(FileState{}), Now: describe(stat(workTree, p))}
		}
	}
	return nil
}

func resolve(ctx context.Context, r *run.Runner, gitDir, name string) (string, error) {
	res, err := r.Run(ctx, &run.Cmd{GitDir: gitDir, Read: true,
		Args: []string{"rev-parse", "-q", "--verify", "--end-of-options", name}})
	var e *run.Error
	if errors.As(err, &e) && e.ExitCode == 1 {
		return "", nil
	}
	return string(bytes.TrimSpace(res.Stdout)), err
}

// symbolic returns the branch HEAD names, "" when detached.
func symbolic(ctx context.Context, r *run.Runner, gitDir string) (string, error) {
	res, err := r.Run(ctx, &run.Cmd{GitDir: gitDir, Read: true, Args: []string{"symbolic-ref", "-q", "HEAD"}})
	var e *run.Error
	if errors.As(err, &e) && e.ExitCode == 1 {
		return "", nil
	}
	return string(bytes.TrimSpace(res.Stdout)), err
}

func orDetached(b string) string {
	if b == "" {
		return "detached"
	}
	return strings.TrimPrefix(b, "refs/heads/")
}

func short(oid string) string {
	if oid == "" {
		return "none"
	}
	return oid[:min(len(oid), 12)]
}

func describe(f FileState) string {
	if !f.Exists {
		return "absent"
	}
	return strconv.FormatInt(f.Size, 10) + " bytes, modified " + f.ModTime.Format(time.DateTime)
}

// BackupPrefix holds the refs kept before a history rewrite, one
// directory per rewrite.
const BackupPrefix = "refs/ramus/backup/"

// backup points BackupPrefix<time>/<ref> at each ref's current value, in
// one transaction, and returns the directory.
func backup(ctx context.Context, r *run.Runner, gitDir string, names []string) (string, error) {
	dir := BackupPrefix + strconv.FormatInt(time.Now().UnixNano(), 10) + "/"
	var in strings.Builder
	for _, n := range names {
		oid, err := resolve(ctx, r, gitDir, n)
		if err != nil {
			return "", err
		}
		if oid == "" {
			continue
		}
		fmt.Fprintf(&in, "create %s%s %s\n", dir, strings.TrimPrefix(n, "refs/"), oid)
	}
	if in.Len() == 0 {
		return "", nil
	}
	_, err := r.Run(ctx, &run.Cmd{GitDir: gitDir, Stdin: strings.NewReader(in.String()),
		Args: []string{"update-ref", "-m", "ramus backup", "--stdin"}})
	return dir, err
}
