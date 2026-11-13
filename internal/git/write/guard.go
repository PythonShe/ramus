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
	// Head is the commit HEAD was on (hex), "" for unborn.
	Head string
	// Refs maps each ref the action moves or deletes to the value seen,
	// "" for a ref that did not exist.
	Refs map[string]string
	// Files are the work tree files at stake as they were seen.
	Files []FileState
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

// Observe records paths as they are now, for an Expect.
func Observe(workTree string, paths []string) []FileState {
	out := make([]FileState, len(paths))
	for i, p := range paths {
		out[i] = stat(workTree, p)
	}
	return out
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
	head, err := resolve(ctx, r, gitDir, "HEAD")
	if err != nil {
		return err
	}
	if head != x.Head {
		return &StaleError{What: "HEAD", Saw: short(x.Head), Now: short(head)}
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
