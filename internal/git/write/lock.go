package write

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// LockedError is a write that could not start, or that git refused,
// because a lock exists (index.lock, or that of a ref): another git
// command is writing, or one crashed and left its lock. The app says so and shows Holders; it
// never deletes the lock unless the user asks, after seeing this.
type LockedError struct {
	Path string
	Age  time.Duration
	// Holders are the processes that have the lock open, where the system
	// can tell (lsof); none means the lock was likely left by a crash.
	Holders []Holder
	// Unknown is set when the system could not tell who holds the lock.
	Unknown error
}

// Holder is a process holding a lock.
type Holder struct {
	PID     int
	Command string
}

func (e *LockedError) Error() string {
	who := "no process holds it; a git command may have crashed"
	if e.Age < LeftAfter {
		who = "no process holds it open; a git command may be running a hook"
	}
	if e.Unknown != nil {
		who = "who holds it cannot be told (" + e.Unknown.Error() + ")"
	}
	if len(e.Holders) > 0 {
		var names []string
		for _, h := range e.Holders {
			names = append(names, fmt.Sprintf("%s (%d)", h.Command, h.PID))
		}
		who = "held by " + strings.Join(names, ", ")
	}
	return fmt.Sprintf("write: %s exists, %v old, %s", e.Path, e.Age.Round(time.Second), who)
}

// lockHeld is called each time waitLock finds the lock held, for a test
// to know a write waits on it rather than has not come to it yet.
var lockHeld atomic.Pointer[func()]

// waitLock waits for gitDir/index.lock to go, up to wait.
func waitLock(ctx context.Context, gitDir string, wait time.Duration) error {
	path := filepath.Join(gitDir, "index.lock")
	deadline := time.Now().Add(wait)
	for {
		if _, err := os.Lstat(path); err != nil {
			return nil
		}
		if f := lockHeld.Load(); f != nil {
			(*f)()
		}
		if time.Now().After(deadline) {
			return describeLock(ctx, path)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// lockError recognizes a write git refused over a lock it could not
// take: index.lock, or that of a ref, packed-refs or config. Its message
// names the lock in quotes (the rest is translated), and the lock is
// still there, since git removes its own on exit.
func lockError(gitDir, stderr string) error {
	for _, m := range quotedLock.FindAllStringSubmatch(stderr, -1) {
		path := m[1]
		if !inRepository(gitDir, path) {
			continue
		}
		if _, err := os.Lstat(path); err != nil {
			continue // gone since
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return describeLock(ctx, path)
	}
	return nil
}

// quotedLock is a lock file git names in a message, as in "Unable to
// create '/r/.git/refs/heads/main.lock': File exists."
var quotedLock = regexp.MustCompile(`'([^'\n]+\.lock)'`)

// inRepository reports a lock file in gitDir or in the common directory
// a linked worktree's gitDir points to, the only places a lock the app
// may remove can be.
func inRepository(gitDir, path string) bool {
	if !filepath.IsAbs(path) || !strings.HasSuffix(path, ".lock") {
		return false
	}
	dirs := []string{gitDir}
	if b, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		common := strings.TrimSpace(string(b))
		if !filepath.IsAbs(common) {
			common = filepath.Join(gitDir, common)
		}
		dirs = append(dirs, common)
	}
	for _, d := range dirs {
		if within(d, path) {
			return true
		}
		// git may print the path through a symlink the app resolved, or
		// the other way round (/var and /private/var on macOS).
		rd, err1 := filepath.EvalSymlinks(d)
		rp, err2 := filepath.EvalSymlinks(filepath.Dir(path))
		if err1 == nil && err2 == nil && within(rd, filepath.Join(rp, filepath.Base(path))) {
			return true
		}
	}
	return false
}

func within(dir, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	return err == nil && rel != "." && !filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func describeLock(ctx context.Context, path string) error {
	e := &LockedError{Path: path}
	if st, err := os.Lstat(path); err == nil {
		e.Age = time.Since(st.ModTime())
	}
	e.Holders, e.Unknown = holders(ctx, path)
	return e
}

// errNoLsof is a system without lsof at a known place.
var errNoLsof = errors.New("no lsof")

// holders asks lsof who has path open. An error means it could not tell,
// which is not the same as no one.
func holders(ctx context.Context, path string) ([]Holder, error) {
	var lsof string
	for _, p := range []string{"/usr/sbin/lsof", "/usr/bin/lsof", "/bin/lsof"} {
		if _, err := os.Stat(p); err == nil {
			lsof = p
			break
		}
	}
	if lsof == "" {
		return nil, errNoLsof
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, lsof, "-Fpc", "--", path).Output()
	var exit *exec.ExitError
	if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1 && len(out) == 0 && ctx.Err() == nil) {
		// lsof exits 1 with no output when no process has the file open.
		return nil, fmt.Errorf("lsof: %w", err)
	}
	var hs []Holder
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "p"):
			pid, _ := strconv.Atoi(line[1:])
			hs = append(hs, Holder{PID: pid})
		case strings.HasPrefix(line, "c") && len(hs) > 0:
			hs[len(hs)-1].Command = line[1:]
		}
	}
	return hs, nil
}

// RemoveLock deletes the lock at path, which a LockedError named, for
// when the user has seen it with no holders and chooses to. It refuses
// a path that is not a lock in the repository at gitDir, and refuses
// while a process holds the lock, or when it cannot tell who does.
func RemoveLock(ctx context.Context, gitDir, path string) error {
	if !inRepository(gitDir, path) {
		return fmt.Errorf("write: %s is not a lock of this repository", path)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	age := time.Since(fi.ModTime())
	if hs, err := holders(ctx, path); len(hs) > 0 || err != nil || age < LeftAfter {
		return &LockedError{Path: path, Age: age, Holders: hs, Unknown: err}
	}
	return os.Remove(path)
}

// LeftAfter is how old a lock no process has open must be to be taken
// for one a git that ended left behind: git closes the lock it writes
// while its hooks run, so a younger one may belong to a commit still
// running a hook.
const LeftAfter = 2 * time.Minute

// Left reports whether the lock is one a git that ended left behind,
// which RemoveLock removes.
func (e *LockedError) Left() bool {
	return len(e.Holders) == 0 && e.Unknown == nil && e.Age >= LeftAfter
}
