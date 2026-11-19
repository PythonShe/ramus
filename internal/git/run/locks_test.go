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
	removeLocks(gitDir, since)
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
}

// A write stopped while git holds the index lock leaves no lock behind,
// on Windows too, where git is ended without a chance to remove it.
func TestStoppedWriteLeavesNoLock(t *testing.T) {
	r := gittest.Init(t)
	r.Commit("one")
	hooks := filepath.Join(r.GitDir(), "hooks")
	os.MkdirAll(hooks, 0o755)
	if err := os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r.Write("a", "1\n")
	r.Git("add", "a")
	lock := filepath.Join(r.GitDir(), "index.lock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for ctx.Err() == nil {
			if _, err := os.Stat(lock); err == nil {
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	_, err := runner(t, 1).Run(ctx, &Cmd{GitDir: r.GitDir(), WorkTree: r.Dir, Args: []string{"commit", "-m", "two"}, Timeout: 20 * time.Second})
	if err == nil {
		t.Fatal("ran to the end")
	}
	if _, err := os.Stat(lock); err == nil {
		t.Fatal("index.lock left")
	}
}
