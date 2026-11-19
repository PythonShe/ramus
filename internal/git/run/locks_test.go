package run

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ramus/internal/git/gittest"
)

// The locks made since a command started go, in its git directory, the
// common one and under refs/; older ones and other files stay.
func TestRemoveLocks(t *testing.T) {
	common := t.TempDir()
	gitDir := filepath.Join(common, "worktrees", "wt")
	write := func(path string, age time.Duration) string {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(-age)
		os.Chtimes(path, at, at)
		return path
	}
	write(filepath.Join(gitDir, "commondir"), 0)
	if err := os.WriteFile(filepath.Join(gitDir, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	since := time.Now().Add(-time.Minute)
	gone := []string{
		write(filepath.Join(gitDir, "index.lock"), 0),
		write(filepath.Join(gitDir, "HEAD.lock"), 0),
		write(filepath.Join(common, "packed-refs.lock"), 0),
		write(filepath.Join(common, "refs", "heads", "topic", "a.lock"), 0),
	}
	kept := []string{
		write(filepath.Join(gitDir, "old.lock"), time.Hour),
		write(filepath.Join(common, "refs", "heads", "main"), 0),
		write(filepath.Join(common, "objects", "pack", "x.lock"), 0),
	}
	removeLocks(gitDir, since, false)
	for _, p := range gone {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s left", p)
		}
	}
	for _, p := range kept {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s removed", p)
		}
	}

	// With another write running in the repository, as in another
	// worktree, its shared locks may be that write's: only this
	// worktree's own go.
	gone = []string{
		write(filepath.Join(gitDir, "index.lock"), 0),
		write(filepath.Join(gitDir, "HEAD.lock"), 0),
	}
	kept = []string{
		write(filepath.Join(common, "packed-refs.lock"), 0),
		write(filepath.Join(common, "refs", "heads", "topic", "a.lock"), 0),
		write(filepath.Join(common, "config.lock"), 0),
	}
	removeLocks(gitDir, since, true)
	for _, p := range gone {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("shared: %s left", p)
		}
	}
	for _, p := range kept {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("shared: %s removed", p)
		}
	}
}

// Writes are counted per repository, a linked worktree's with its main
// one's, so that a stopped write can tell whether another runs there.
func TestWritesPerRepository(t *testing.T) {
	common := t.TempDir()
	wt := filepath.Join(common, "worktrees", "wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if commonDir(wt) != filepath.Clean(common) || commonDir(common) != filepath.Clean(common) {
		t.Fatalf("common of %s is %s", wt, commonDir(wt))
	}
	var w writes
	w.add(commonDir(wt), 1)
	if w.others(common) {
		t.Fatal("one write counted as two")
	}
	w.add(commonDir(common), 1)
	if !w.others(commonDir(wt)) {
		t.Fatal("a write in the main worktree not seen from the linked one")
	}
	w.add(common, -1)
	w.add(common, -1)
	if len(w.n) != 0 {
		t.Fatalf("left %v", w.n)
	}
}

// A write of the same repository that comes while a stopped write's
// locks are removed waits for the removal: the locks its git takes are
// not taken for the stopped one's. A write elsewhere does not wait.
func TestNoWriteStartsWhileStoppedLocksGo(t *testing.T) {
	common := t.TempDir()
	stoppedDir := filepath.Join(common, "worktrees", "a")
	if err := os.MkdirAll(stoppedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stoppedDir, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var w writes
	w.add(commonDir(stoppedDir), 1)
	since := time.Now().Add(-time.Second)
	locks := []string{"index.lock", "config.lock", "packed-refs.lock", "refs/heads/topic.lock"}
	// The other write, in the main worktree, takes its locks as soon as
	// it is counted in.
	admitted := make(chan struct{})
	taken := make(chan error, 1)
	other := func() {
		w.add(commonDir(common), 1)
		close(admitted)
		var err error
		for _, rel := range locks {
			p := filepath.Join(common, filepath.FromSlash(rel))
			os.MkdirAll(filepath.Dir(p), 0o755)
			if e := os.WriteFile(p, nil, 0o644); e != nil {
				err = e
			}
		}
		taken <- err
	}
	elsewhere := make(chan struct{})
	w.stopped(commonDir(stoppedDir), func(shared bool) {
		if shared {
			t.Error("another write counted before it came")
		}
		go other()
		go func() {
			w.add(t.TempDir(), 1)
			close(elsewhere)
		}()
		select {
		case <-elsewhere:
		case <-time.After(10 * time.Second):
			t.Error("a write in another repository waits for the removal")
		}
		select {
		case <-admitted:
			t.Error("a write of the same repository counted in during the removal")
		case <-time.After(200 * time.Millisecond):
		}
		removeLocks(stoppedDir, since, shared)
	})
	select {
	case <-admitted:
	case <-time.After(10 * time.Second):
		t.Fatal("the other write never counted in")
	}
	if err := <-taken; err != nil {
		t.Fatal(err)
	}
	for _, rel := range locks {
		if _, err := os.Stat(filepath.Join(common, filepath.FromSlash(rel))); err != nil {
			t.Errorf("the other write's %s: %v", rel, err)
		}
	}
	if !w.others(common) {
		t.Fatal("the other write is not counted")
	}
}

// A write stopped while git holds the index lock leaves no lock behind,
// on Windows too, where git is ended without a chance to remove it.
func TestStoppedWriteLeavesNoLock(t *testing.T) {
	r := gittest.Init(t)
	r.Commit("one")
	hooks := filepath.Join(r.GitDir(), "hooks")
	os.MkdirAll(hooks, 0o755)
	// The hook says when it runs: git commit -a keeps the index locked
	// through its hooks, and by then has the lock down for removal. Stopped
	// in the instant it makes a lock, before that, git itself leaves it.
	running := filepath.Join(t.TempDir(), "running")
	hook := "#!/bin/sh\n: > '" + filepath.ToSlash(running) + "'\nsleep 30\n"
	if err := os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	r.Write("a", "1\n")
	r.Git("add", "a")
	lock := filepath.Join(r.GitDir(), "index.lock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for ctx.Err() == nil {
			if _, err := os.Stat(running); err == nil {
				if _, err := os.Stat(lock); err != nil {
					t.Error("git holds no index.lock while its hook runs")
				}
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	_, err := runner(t, 1).Run(ctx, &Cmd{GitDir: r.GitDir(), WorkTree: r.Dir, Args: []string{"commit", "-a", "-m", "two"}, Timeout: 20 * time.Second})
	if err == nil {
		t.Fatal("ran to the end")
	}
	if _, err := os.Stat(lock); err == nil {
		t.Fatal("index.lock left")
	}
}

// Where git removes its own locks when stopped (all but Windows), a
// write stopped leaves alone the locks it did not make: one a git in a
// terminal took while the write ran is live, and removing it would break
// that git's write. A lock git leaves in the instant it makes one, before
// it has it down for removal, is told to the user as any other, and can
// be removed once nothing has it open (write.LockedError, RemoveLock).
func TestStoppedWriteLeavesOthersLocks(t *testing.T) {
	if stopsHard {
		t.Skip("git is stopped without removing its locks: the app removes those made since")
	}
	r := gittest.Init(t)
	r.Commit("one")
	hooks := filepath.Join(r.GitDir(), "hooks")
	os.MkdirAll(hooks, 0o755)
	running := filepath.Join(t.TempDir(), "running")
	hook := "#!/bin/sh\n: > '" + filepath.ToSlash(running) + "'\nsleep 30\n"
	if err := os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	r.Write("a", "1\n")
	r.Git("add", "a")
	others := []string{
		filepath.Join(r.GitDir(), "refs", "heads", "other.lock"),
		filepath.Join(r.GitDir(), "config.lock"),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	taken := make(chan error, 1)
	go func() {
		// The hook says when the write is under way; the locks another
		// git takes then are newer than the write.
		for ctx.Err() == nil {
			if _, err := os.Stat(running); err == nil {
				var err error
				for _, p := range others {
					if e := os.WriteFile(p, nil, 0o644); e != nil {
						err = e
					}
				}
				taken <- err
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		taken <- ctx.Err()
	}()
	_, err := runner(t, 1).Run(ctx, &Cmd{GitDir: r.GitDir(), WorkTree: r.Dir, Args: []string{"commit", "-a", "-m", "two"}, Timeout: 20 * time.Second})
	if err == nil {
		t.Fatal("ran to the end")
	}
	cancel()
	if err := <-taken; err != nil {
		t.Fatalf("the hook never ran, or the locks were not made: %v", err)
	}
	for _, p := range others {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s, another git's, removed: %v", filepath.Base(p), err)
		}
	}
	if _, err := os.Stat(filepath.Join(r.GitDir(), "index.lock")); err == nil {
		t.Error("index.lock, the write's own, left")
	}
}
