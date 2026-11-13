package write

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLockWaitsForAnotherWriter(t *testing.T) {
	r, q, _ := newQueue(t)
	lock := filepath.Join(r.GitDir(), "index.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	j := q.Submit(commitOp(r, "after lock"))
	time.Sleep(200 * time.Millisecond)
	select {
	case <-j.Done():
		t.Fatal("ran while index.lock existed")
	default:
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
	if lsofMissing() {
		t.Skip("no lsof")
	}
	if len(le.Holders) != 1 || le.Holders[0].PID != os.Getpid() {
		t.Fatalf("holders %+v, want this process", le.Holders)
	}
	if err := RemoveLock(context.Background(), r.GitDir()); !errors.As(err, &le) {
		t.Fatalf("removed a held lock: %v", err)
	}
	f.Close()
	if err := RemoveLock(context.Background(), r.GitDir()); err != nil {
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
}

func lsofMissing() bool {
	for _, p := range []string{"/usr/sbin/lsof", "/usr/bin/lsof", "/bin/lsof"} {
		if _, err := os.Stat(p); err == nil {
			return false
		}
	}
	return true
}
