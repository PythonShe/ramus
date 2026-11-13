package write

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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
	AnyHead bool
}

func checkHead(ctx context.Context, r *run.Runner, gitDir string, x *Expect) error {
	branch, err := symbolic(ctx, r, gitDir)
	if err != nil {
		return err
	}
	if branch != x.Branch {
		return &StaleError{What: "the current branch", Saw: orDetached(x.Branch), Now: orDetached(branch)}
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
			rel, _ := filepath.Rel(workTree, path)
			out = append(out, stat(workTree, filepath.ToSlash(rel)))
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
	for name, saw := range x.Refs {
		now, err := resolve(ctx, r, gitDir, name)
		if err != nil {
			return err
		}
		if now != saw {
			return &StaleError{What: name, Saw: short(saw), Now: short(now)}
		}
	}
	for _, f := range x.Files {
		if now := stat(workTree, f.Path); !now.same(f) {
			return &StaleError{What: f.Path, Saw: describe(f), Now: describe(now)}
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
