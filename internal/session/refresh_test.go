package session

import (
	"testing"
	"time"

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
