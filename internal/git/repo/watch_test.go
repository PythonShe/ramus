package repo

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/watch"
)

func TestClassifyGit(t *testing.T) {
	for rel, want := range map[string]Change{
		"HEAD":                         RefsChanged,
		"refs/heads/main":              RefsChanged,
		"refs/heads/main.lock":         0,
		"packed-refs":                  RefsChanged,
		"reftable/tables.list":         RefsChanged,
		"index":                        IndexChanged,
		"index.lock":                   0,
		"MERGE_HEAD":                   OperationChanged | RefsChanged,
		"rebase-merge/done":            OperationChanged | RefsChanged,
		"config":                       ConfigChanged,
		"objects/info/commit-graphs/x": GraphChanged,
		"shallow":                      GraphChanged,
		"shallow.lock":                 0,
		"info/grafts":                  GraphChanged,
		"objects/ab/cdef":              0,
		"logs/HEAD":                    0,
		"worktrees/wt/HEAD":            RefsChanged,
		"worktrees/wt/index":           0,
		"worktrees/wt/logs/HEAD":       0,
		"worktrees":                    RefsChanged,
		"worktrees/wt":                 RefsChanged,
		"worktrees/wt/gitdir":          RefsChanged,
		"worktrees/wt/locked":          RefsChanged,
		"COMMIT_EDITMSG":               0,
	} {
		if got := classifyGit(rel); got != want {
			t.Errorf("%s: %b, want %b", rel, got, want)
		}
	}
}

func next(t *testing.T, ch <-chan Notice, want Change) Notice {
	t.Helper()
	deadline := time.After(5 * time.Second)
	var got Change
	for {
		select {
		case n := <-ch:
			got |= n.What
			if n.What&(want|Rescan) != 0 {
				return n
			}
		case <-deadline:
			t.Fatalf("no %b notice (got %b)", want, got)
		}
	}
}

// ready makes a change until the watch reports what, so the watch has
// surely started however long the system takes to start it, then drains
// notices until none came for a while.
func ready(t *testing.T, ch <-chan Notice, what Change, change func()) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for got := false; !got; {
		change()
		select {
		case n := <-ch:
			got = n.What&(what|Rescan) != 0
		case <-time.After(200 * time.Millisecond):
		}
		if !got && time.Now().After(deadline) {
			t.Fatal("the watch never started")
		}
	}
	for {
		select {
		case <-ch:
		case <-time.After(300 * time.Millisecond):
			return
		}
	}
}

func TestWatch(t *testing.T) {
	g := gittest.Init(t)
	g.Commit("one")
	r, err := Open(context.Background(), testrun.Runner(t), g.Dir, trustAll)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := r.Watch(nil)
	if err == watch.ErrUnsupported {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	ready(t, ch, WorkTreeChanged, func() { g.Write(".ready", time.Now().String()) })
	g.Write("file.txt", "x")
	if n := next(t, ch, WorkTreeChanged); n.What&Rescan == 0 && len(n.Paths) == 0 {
		t.Error("work tree change without its path")
	}
	g.Git("branch", "new")
	next(t, ch, RefsChanged)
	g.Git("add", "file.txt")
	next(t, ch, IndexChanged)
	r.Close()
	select {
	case <-r.Life().Done():
	case <-time.After(5 * time.Second):
		t.Fatal("close did not finish")
	}
}

// A linked worktree's git directory lives in the main repository; its
// changes are seen through a second watcher.
func TestWatchLinkedWorktree(t *testing.T) {
	g := gittest.Init(t)
	g.Commit("one")
	wt := filepath.Join(t.TempDir(), "wt")
	g.Git("worktree", "add", "-q", "-b", "side", wt)
	r, err := Open(context.Background(), testrun.Runner(t), wt, trustAll)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { r.Close(); <-r.Life().Done() }()
	ch, err := r.Watch(nil)
	if err == watch.ErrUnsupported {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	ready(t, ch, WorkTreeChanged, func() { os.WriteFile(filepath.Join(wt, ".ready"), []byte(time.Now().String()), 0o644) })
	// The main repository's git directory, watched apart: its config
	// written again as it is.
	config := filepath.Join(g.GitDir(), "config")
	b, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	ready(t, ch, ConfigChanged, func() { os.WriteFile(config, b, 0o644) })
	w := gittest.Open(t, wt)
	w.Commit("on side")
	next(t, ch, RefsChanged)
	os.WriteFile(filepath.Join(wt, "f"), []byte("x"), 0o644)
	next(t, ch, WorkTreeChanged)
}
