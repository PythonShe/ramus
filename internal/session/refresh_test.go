package session

import (
	"context"
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
	runner := run.New(g, gittest.Env(), 8)
	statusRuns := func() (full, rechecks int) {
		data, _ := os.ReadFile(logFile)
		for _, l := range strings.Split(string(data), "\n") {
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
