package session

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/gitexe"
	"ramus/internal/git/run"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/repo"
)

// loggedRunner runs git through a script that logs each command line;
// logged returns the lines so far.
func loggedRunner(t *testing.T) (runner *run.Runner, logged func() []string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as git")
	}
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip(err)
	}
	bin := t.TempDir()
	logFile := filepath.Join(bin, "log")
	script := "#!/bin/sh\necho \"$*\" >> " + logFile + "\nexec " + real + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	g, err := gitexe.Find(context.Background(), bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err != nil {
		t.Fatal(err)
	}
	return run.New(g, gittest.Env(), 8), func() []string {
		data, _ := os.ReadFile(logFile)
		return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	}
}

// pump runs what the session hands the main thread for d.
func (m mainThread) pump(d time.Duration) {
	end := time.After(d)
	for {
		select {
		case fn := <-m:
			fn()
		case <-end:
			return
		}
	}
}

// A notice that the index changed runs status only when the index's
// content key did, and refs read with the same values keep the list
// history and labels were built from.
func TestRefreshKeyedOnContent(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a", "1")
	r.Commit("one")
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "status", func() bool { return s.State.Status != nil && s.State.Refs != nil })
	m.pump(4 * Debounce) // the watcher's notices of the open itself

	read, list := s.State.Refreshed, s.State.Refs
	s.Refresh(repo.IndexChanged)
	m.pump(4 * Debounce)
	if !s.State.Refreshed.Equal(read) {
		t.Fatal("status ran again for an index that did not change")
	}

	s.Refresh(repo.RefsChanged)
	m.until(t, "the refs read again", func() bool { return !s.State.Refreshed.Equal(read) })
	if s.State.Refs != list {
		t.Fatal("refs with the same values made a new list")
	}

	read = s.State.Refreshed
	r.Write("b", "2")
	r.Git("add", "b")
	m.until(t, "the staged file", func() bool { return !s.State.Refreshed.Equal(read) && s.State.Status.Len() == 1 })

	r.Git("branch", "topic")
	m.until(t, "the new branch", func() bool { return s.State.Refs != list && s.State.Refs.Len() == 2 })
}

// An edit to a file git knows is read on its own, and the status that
// results is the one git gives; a new file reads the whole work tree.
func TestRefreshRechecksEditedFiles(t *testing.T) {
	runner, logged := loggedRunner(t)
	statusRuns := func() (full, rechecks int) {
		for _, l := range logged() {
			if !strings.Contains(l, " status --porcelain=v2") {
				continue
			}
			if strings.Contains(l, "--ignore-submodules=dirty -- ") {
				rechecks++
			} else {
				full++
			}
		}
		return
	}

	r := gittest.Init(t)
	r.Write("a", "1")
	r.Write("b", "1")
	r.Commit("one")
	m := make(mainThread, 64)
	s := Open(runner, trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "status", func() bool { return s.State.Status != nil })
	m.pump(4 * Debounce)
	full0, re0 := statusRuns()

	r.Write("a", "2")
	m.until(t, "the edit", func() bool { return s.State.Status.Len() == 1 })
	m.pump(4 * Debounce)
	full1, re1 := statusRuns()
	if full1 != full0 || re1 == re0 {
		t.Fatalf("an edit ran %d full status and %d rechecks", full1-full0, re1-re0)
	}
	want := strings.TrimRight(r.Git("status", "--porcelain"), "\n")
	if got := string(s.State.Status.Path(0)); want != " M a" || got != "a" {
		t.Fatalf("status %q, git %q", got, want)
	}

	r.Write("c", "new")
	m.until(t, "the new file", func() bool { return s.State.Status.Len() == 2 })
	if full2, _ := statusRuns(); full2 == full1 {
		t.Fatal("a new file was placed without a full status")
	}
}

// A watcher that dropped notices asks for a rescan: the paths it named
// before no longer stand for everything that changed.
func TestOverflowRescans(t *testing.T) {
	s := &Session{wake: make(chan struct{}, 1)}
	s.refreshPaths(repo.WorkTreeChanged, []string{"a"})
	if s.dirtyAll || len(s.dirtyPaths) != 1 {
		t.Fatalf("named paths: all %v, paths %v", s.dirtyAll, s.dirtyPaths)
	}
	s.refreshPaths(repo.Rescan, nil)
	if !s.dirtyAll || s.dirtyPaths != nil || s.dirty&repo.Rescan == 0 {
		t.Fatalf("after overflow: all %v, paths %v, what %v", s.dirtyAll, s.dirtyPaths, s.dirty)
	}
	s.refreshPaths(repo.WorkTreeChanged, []string{"b"})
	if !s.dirtyAll || s.dirtyPaths != nil {
		t.Fatal("a later notice narrowed the rescan")
	}
}

// The window gaining focus reads refs and the index's key, and status
// only when they changed; with no watcher, everything.
func TestFocusRefresh(t *testing.T) {
	runner, logged := loggedRunner(t)
	statusRuns := func() int {
		n := 0
		for _, l := range logged() {
			if strings.Contains(l, " status --porcelain=v2") {
				n++
			}
		}
		return n
	}
	r := gittest.Init(t)
	r.Write("a", "1")
	r.Commit("one")
	m := make(mainThread, 64)
	s := Open(runner, trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "status", func() bool { return s.State.Status != nil })
	m.pump(4 * Debounce)

	before := statusRuns()
	s.FocusRefresh()
	m.pump(4 * Debounce)
	if n := statusRuns() - before; n != 0 {
		t.Fatalf("focus with nothing changed ran status %d times", n)
	}

	s.State.WatchErr = errors.New("not watching")
	s.FocusRefresh()
	m.pump(4 * Debounce)
	if n := statusRuns() - before; n != 1 {
		t.Fatalf("focus with no watcher ran status %d times", n)
	}

	// What the check finds, whatever the watcher saw.
	rp := s.Repo
	var seen refreshSeen
	if got := focusChanges(rp, &seen); got&(repo.IndexChanged|repo.RefsChanged) != repo.IndexChanged|repo.RefsChanged {
		t.Fatalf("nothing seen yet: %v", got)
	}
	look := func() {
		seen.index, _ = repo.ReadIndexKey(rp.GitDir)
		seen.indexRead = true
		h, _ := rp.Refs().Head()
		seen.head = &h
		seen.refs, _ = rp.Refs().List()
	}
	look()
	if got := focusChanges(rp, &seen); got != 0 {
		t.Fatalf("nothing changed: %v", got)
	}
	r.Git("branch", "topic")
	if got := focusChanges(rp, &seen); got&repo.RefsChanged == 0 {
		t.Fatalf("a new branch: %v", got)
	}
	look()
	r.Write("b", "2")
	r.Git("add", "b")
	if got := focusChanges(rp, &seen); got != repo.IndexChanged {
		t.Fatalf("a staged file: %v", got)
	}
}

// A config change made elsewhere is read again; Reload drops what was
// kept and loads history again.
func TestReload(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a", "1")
	r.Commit("one")
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "status", func() bool { return s.State.Status != nil && s.State.Refs != nil })

	r.Git("config", "diff.tool", "elsewhere")
	m.until(t, "the new config", func() bool {
		v, _ := s.Repo.Config().Get("diff.tool")
		return v == "elsewhere"
	})

	s.LoadHistory()
	m.until(t, "history", func() bool { return s.History.Done })
	view := s.History.View
	head := s.State.Head.OID
	done := false
	s.LoadCommit(head, func(d CommitDetail) { done = d.Done })
	m.until(t, "the commit", func() bool { return done })

	s.Reload()
	done = false
	s.LoadCommit(head, func(d CommitDetail) { done = d.Done })
	if done {
		t.Fatal("a commit kept past Reload")
	}
	m.until(t, "the commit again", func() bool { return done })
	s.LoadHistory()
	m.until(t, "history again", func() bool { return s.History.Done && s.History.View != view })
}
