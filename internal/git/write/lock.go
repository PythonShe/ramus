package write

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// LockedError is a write that could not start, or that git refused,
// because index.lock exists: another git command is writing the index, or
// one crashed and left its lock. The app says so and shows Holders; it
// never deletes the lock unless the user asks, after seeing this.
type LockedError struct {
	Path string
	Age  time.Duration
	// Holders are the processes that have the lock open, where the system
	// can tell (lsof); none means the lock was likely left by a crash.
	Holders []Holder
}

// Holder is a process holding a lock.
type Holder struct {
	PID     int
	Command string
}

func (e *LockedError) Error() string {
	who := "no process holds it; a git command may have crashed"
	if len(e.Holders) > 0 {
		var names []string
		for _, h := range e.Holders {
			names = append(names, fmt.Sprintf("%s (%d)", h.Command, h.PID))
		}
		who = "held by " + strings.Join(names, ", ")
	}
	return fmt.Sprintf("write: %s exists, %v old, %s", e.Path, e.Age.Round(time.Second), who)
}

// waitLock waits for gitDir/index.lock to go, up to wait.
func waitLock(ctx context.Context, gitDir string, wait time.Duration) error {
	path := filepath.Join(gitDir, "index.lock")
	deadline := time.Now().Add(wait)
	for {
		if _, err := os.Lstat(path); err != nil {
			return nil
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

// lockError recognizes a write git refused over the lock: its message
// names index.lock (the rest is translated) and the lock is still there,
// since git removes its own on exit.
func lockError(gitDir, stderr string) error {
	if !strings.Contains(stderr, "index.lock") {
		return nil
	}
	path := filepath.Join(gitDir, "index.lock")
	if _, err := os.Lstat(path); err != nil {
		return nil // gone since
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return describeLock(ctx, path)
}

func describeLock(ctx context.Context, path string) error {
	e := &LockedError{Path: path}
	if st, err := os.Lstat(path); err == nil {
		e.Age = time.Since(st.ModTime())
	}
	e.Holders = holders(ctx, path)
	return e
}

// holders asks lsof, where there is one at a known place, who has path
// open.
func holders(ctx context.Context, path string) []Holder {
	var lsof string
	for _, p := range []string{"/usr/sbin/lsof", "/usr/bin/lsof", "/bin/lsof"} {
		if _, err := os.Stat(p); err == nil {
			lsof = p
			break
		}
	}
	if lsof == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, lsof, "-Fpc", "--", path).Output()
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
	return hs
}

// RemoveLock deletes index.lock, for when the user has seen a LockedError
// with no holders and chooses to. It refuses while a process holds it.
func RemoveLock(ctx context.Context, gitDir string) error {
	path := filepath.Join(gitDir, "index.lock")
	if hs := holders(ctx, path); len(hs) > 0 {
		return &LockedError{Path: path, Holders: hs}
	}
	return os.Remove(path)
}
