package write

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"ramus/internal/git/run"
)

func TestLockWaitsForAnotherWriter(t *testing.T) {
	r, q, _ := newQueue(t)
	lock := filepath.Join(r.GitDir(), "index.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	held := make(chan struct{}, 1)
	signal := func() {
		select {
		case held <- struct{}{}:
		default:
		}
	}
	lockHeld.Store(&signal)
	t.Cleanup(func() { lockHeld.Store(nil) })
	j := q.Submit(commitOp(r, "after lock"))
	// Waiting on the lock, twice: it found it held and looked again.
	for range 2 {
		select {
		case <-held:
		case <-j.Done():
			t.Fatal("ran while index.lock existed")
		case <-time.After(time.Minute):
			t.Fatal("the write never came to the lock")
		}
	}
	os.Remove(lock)
	if _, err := wait(t, j); err != nil {
		t.Fatal(err)
	}
}

func TestLockedError(t *testing.T) {
	r, q, _ := newQueue(t)
	q.LockWait = 100 * time.Millisecond
	lock := filepath.Join(r.GitDir(), "index.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := wait(t, q.Submit(commitOp(r, "blocked")))
	var le *LockedError
	if !errors.As(err, &le) || le.Path != lock || len(le.Holders) != 0 {
		t.Fatalf("stale lock: %v", err)
	}

	f, err := os.Open(lock)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, err = wait(t, q.Submit(commitOp(r, "blocked")))
	if !errors.As(err, &le) {
		t.Fatalf("held lock: %v", err)
	}
	if !holdersKnown() {
		t.Skip("neither lsof nor /proc")
	}
	if len(le.Holders) != 1 || le.Holders[0].PID != os.Getpid() {
		t.Fatalf("holders %+v, want this process", le.Holders)
	}
	if err := RemoveLock(context.Background(), r.GitDir(), lock); !errors.As(err, &le) {
		t.Fatalf("removed a held lock: %v", err)
	}
	f.Close()
	// Fresh, it may be a commit's running a hook, which has it closed.
	if err := RemoveLock(context.Background(), r.GitDir(), lock); !errors.As(err, &le) || le.Left() {
		t.Fatalf("removed a fresh lock: %v", err)
	}
	old := time.Now().Add(-LeftAfter - time.Second)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	if err := RemoveLock(context.Background(), r.GitDir(), lock); err != nil {
		t.Fatal(err)
	}
	if _, err := wait(t, q.Submit(commitOp(r, "free"))); err != nil {
		t.Fatal(err)
	}
}

func TestLockErrorFromGit(t *testing.T) {
	dir := t.TempDir()
	msg := "fatal: Unable to create '" + filepath.Join(dir, "index.lock") + "': File exists.\n"
	if lockError(dir, msg) != nil {
		t.Fatal("no lock on disk, yet reported locked")
	}
	os.WriteFile(filepath.Join(dir, "index.lock"), nil, 0o644)
	var le *LockedError
	if !errors.As(lockError(dir, msg), &le) {
		t.Fatal("lock not recognized")
	}
	if lockError(dir, "fatal: something else\n") != nil {
		t.Fatal("unrelated failure reported as locked")
	}

	ref := filepath.Join(dir, "refs", "heads", "main.lock")
	os.MkdirAll(filepath.Dir(ref), 0o755)
	os.WriteFile(ref, nil, 0o644)
	msg = "error: cannot lock ref 'refs/heads/main': Unable to create '" + ref + "': File exists.\n"
	if !errors.As(lockError(dir, msg), &le) || le.Path != ref {
		t.Fatalf("ref lock not recognized: %+v", le)
	}
	outside := filepath.Join(t.TempDir(), "x.lock")
	os.WriteFile(outside, nil, 0o644)
	if lockError(dir, "fatal: Unable to create '"+outside+"': File exists.\n") != nil {
		t.Fatal("a lock outside the repository reported")
	}
	if err := RemoveLock(context.Background(), dir, outside); err == nil {
		t.Fatal("removed a file outside the repository")
	}
	// Nor through a folder in the git directory that links out of it.
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(dir, "out")); err != nil {
		t.Skip("no symlinks here:", err)
	}
	if err := RemoveLock(context.Background(), dir, filepath.Join(dir, "out", "x.lock")); err == nil {
		t.Fatal("removed a file outside the repository through a symlink")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal(err)
	}
}

// A lock that a new git takes while RemoveLock asks who held the old one
// is live, and stays.
func TestRemoveLockLeavesALockTakenSince(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, "index.lock")
	old := time.Now().Add(-LeftAfter - time.Minute)
	if err := os.WriteFile(lock, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	retake := func() {
		os.Remove(lock)
		os.WriteFile(lock, []byte("new"), 0o644)
	}
	lockLooked.Store(&retake)
	t.Cleanup(func() { lockLooked.Store(nil) })
	var le *LockedError
	if err := RemoveLock(context.Background(), dir, lock); !errors.As(err, &le) || le.Left() {
		t.Fatalf("remove of a lock taken again meanwhile: %v", err)
	}
	if b, err := os.ReadFile(lock); err != nil || string(b) != "new" {
		t.Fatalf("the new lock is gone: %q %v", b, err)
	}
}

// Every system tells a lock a process holds from one left behind, lsof
// or not: Windows by an open with no sharing, Linux by /proc. A held one
// is never removed; once let go and old, it is.
func TestRemoveLockTellsAHeldLock(t *testing.T) {
	run := func(t *testing.T) {
		dir := t.TempDir()
		lock := filepath.Join(dir, "index.lock")
		if err := os.WriteFile(lock, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-LeftAfter - time.Minute)
		if err := os.Chtimes(lock, old, old); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(lock)
		if err != nil {
			t.Fatal(err)
		}
		var le *LockedError
		if err := RemoveLock(context.Background(), dir, lock); !errors.As(err, &le) || le.Left() {
			f.Close()
			t.Fatalf("removed a held lock: %v", err)
		}
		if errors.Is(le.Unknown, errNoLsof) {
			f.Close()
			t.Fatal("who holds the lock cannot be told")
		}
		if runtime.GOOS != "windows" && (len(le.Holders) != 1 || le.Holders[0].PID != os.Getpid()) {
			t.Errorf("holders %+v, want this process", le.Holders)
		}
		f.Close()
		if err := RemoveLock(context.Background(), dir, lock); err != nil {
			t.Fatalf("a lock let go: %v", err)
		}
		if _, err := os.Lstat(lock); err == nil {
			t.Fatal("not removed")
		}
	}
	t.Run("system", run)
	t.Run("without lsof", func(t *testing.T) {
		if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
			t.Skip("neither /proc nor Windows")
		}
		saved := lsofPaths
		lsofPaths = nil
		t.Cleanup(func() { lsofPaths = saved })
		run(t)
	})
}

// holdersKnown reports whether this system tells who has a file open, as
// the test expects it: by lsof, or without it by /proc, as Linux does.
// Windows is not looked at here.
func holdersKnown() bool {
	if runtime.GOOS == "windows" {
		return false
	}
	for _, p := range lsofPaths {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	_, err := os.Stat("/proc/self/fd")
	return err == nil
}

func TestFetchDoesNotWaitOnIndexLock(t *testing.T) {
	r, q, _ := newQueue(t)
	r.Commit("one")
	q.LockWait = 100 * time.Millisecond
	if err := os.WriteFile(filepath.Join(r.GitDir(), "index.lock"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	refsOnly := Op{Name: "Fetch", NoIndex: true, Cmd: run.Cmd{GitDir: r.GitDir(), WorkTree: r.Dir, Args: []string{"update-ref", "refs/remotes/o/main", "HEAD"}}}
	if _, err := wait(t, q.Submit(refsOnly)); err != nil {
		t.Fatalf("a write that never takes index.lock waited on it: %v", err)
	}
	var le *LockedError
	if _, err := wait(t, q.Submit(commitOp(r, "blocked"))); !errors.As(err, &le) {
		t.Fatalf("a commit ran past index.lock: %v", err)
	}
}
