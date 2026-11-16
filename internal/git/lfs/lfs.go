// Package lfs reads the Git LFS file locks of a repository through
// git-lfs, which talks to the LFS server with git's own credentials. The
// writes that take and release locks are in package write.
package lfs

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"ramus/internal/git/run"
)

// Lock is a file locked on the LFS server.
type Lock struct {
	ID string
	// Path is the locked file, relative to the work tree, slash-separated.
	Path  string
	Owner string
	At    time.Time
	// Ours is a lock held by the user: the file may be pushed. Known only
	// when the server verifies locks (Locks.Verified).
	Ours bool
}

// Locks is the locks of a repository as the server listed them.
type Locks struct {
	List []Lock
	// Verified says the server told which locks are the user's; when it
	// cannot, Ours is never set.
	Verified bool
}

// ErrNoLFS is returned when git-lfs is not installed.
var ErrNoLFS = errors.New("lfs: git-lfs is not installed")

// Timeout bounds a read of the locks, which asks the server, as many
// times as it pages them.
const Timeout = 2 * time.Minute

// MaxLocks bounds the locks read.
const MaxLocks = 10000

// Uses reports whether the repository keeps files in LFS: a
// .gitattributes at the top of the work tree routes some through its
// filter, or git-lfs has stored objects in the git directory. It reads
// files, so it runs off the main thread.
func Uses(commonDir, workTree string) bool {
	if fi, err := os.Stat(filepath.Join(commonDir, "lfs")); err == nil && fi.IsDir() {
		return true
	}
	if workTree == "" {
		return false
	}
	f, err := os.Open(filepath.Join(workTree, ".gitattributes"))
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "#") {
			continue
		}
		if slices.Contains(strings.Fields(line), "filter=lfs") {
			return true
		}
	}
	return false
}

// List reads the locks from the LFS server through r. It asks the server
// to tell the user's locks from the others' and, when the server cannot,
// lists them without.
func List(ctx context.Context, r *run.Runner, gitDir, workTree string) (Locks, error) {
	res, err := r.Run(ctx, cmd(gitDir, workTree, "--verify"))
	if missing(res, err) {
		return Locks{}, ErrNoLFS
	}
	if err == nil {
		var v struct{ Ours, Theirs []jsonLock }
		if err := json.Unmarshal(res.Stdout, &v); err != nil {
			return Locks{}, err
		}
		ls := Locks{Verified: true}
		for _, l := range v.Ours {
			ls.List = append(ls.List, l.lock(true))
		}
		for _, l := range v.Theirs {
			ls.List = append(ls.List, l.lock(false))
		}
		// git-lfs answers a server without verification with no locks at
		// all, so none is checked against the plain list.
		if len(ls.List) > 0 {
			sortLocks(ls.List)
			return ls, nil
		}
	} else if ctx.Err() != nil || unreachable(res, err) {
		// The plain list asks the same server: it would wait as long.
		return Locks{}, err
	}
	res, err2 := r.Run(ctx, cmd(gitDir, workTree))
	if err2 != nil {
		if err != nil {
			return Locks{}, err
		}
		return Locks{}, err2
	}
	var v []jsonLock
	if err := json.Unmarshal(res.Stdout, &v); err != nil {
		return Locks{}, err
	}
	// The server verified an empty list.
	ls := Locks{Verified: err == nil && len(v) == 0}
	for _, l := range v {
		ls.List = append(ls.List, l.lock(false))
	}
	sortLocks(ls.List)
	return ls, nil
}

func cmd(gitDir, workTree string, extra ...string) *run.Cmd {
	args := append([]string{"lfs", "locks", "--json", "--limit=" + strconv.Itoa(MaxLocks)}, extra...)
	return &run.Cmd{GitDir: gitDir, WorkTree: workTree, Args: args, Read: true,
		Untranslated: true, Timeout: Timeout, MaxOutput: 64 << 20}
}

// missing reports a failure because git has no lfs command.
func missing(res run.Result, err error) bool {
	if err == nil {
		return false
	}
	stderr := res.Stderr
	if e, ok := errors.AsType[*run.Error](err); ok {
		stderr = e.Stderr
	}
	return strings.Contains(stderr, "'lfs' is not a git command")
}

// unreachable reports a failure to reach the server at all, as against
// a server that does not verify locks. A refused login is not one: the
// server may verify for those who can push only, and list to others.
func unreachable(res run.Result, err error) bool {
	if errors.Is(err, run.ErrTimeout) {
		return true
	}
	stderr := res.Stderr
	if e, ok := errors.AsType[*run.Error](err); ok {
		stderr = e.Stderr
	}
	stderr = strings.ToLower(stderr)
	for _, s := range []string{"could not resolve host", "no such host", "connection refused", "timed out", "timeout",
		"no route to host", "network is unreachable"} {
		if strings.Contains(stderr, s) {
			return true
		}
	}
	return false
}

func sortLocks(ls []Lock) {
	slices.SortFunc(ls, func(a, b Lock) int { return strings.Compare(a.Path, b.Path) })
}

// jsonLock is a lock as git-lfs prints it.
type jsonLock struct {
	ID    string `json:"id"`
	Path  string `json:"path"`
	Owner struct {
		Name string `json:"name"`
	} `json:"owner"`
	LockedAt time.Time `json:"locked_at"`
}

func (l jsonLock) lock(ours bool) Lock {
	return Lock{ID: l.ID, Path: l.Path, Owner: l.Owner.Name, At: l.LockedAt, Ours: ours}
}
