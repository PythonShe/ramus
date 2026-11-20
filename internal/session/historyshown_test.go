package session

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

// loaded waits for the history load running to end, its history handed
// to the main thread but not yet taken by it.
func (h *hist) loaded() {
	h.mu.Lock()
	done := h.done
	h.mu.Unlock()
	if done != nil {
		<-done
	}
}

// A load overtaken after it grew the history, and before the main thread
// took what it grew, leaves the history shown as it was: the load after
// it lays its rows over that one or lays history out anew, never over the
// rows of the load that was dropped. The view places itself by the base,
// and lost its place and its choice over one it never had.
func TestHistoryGrowsOnlyOnTheHistoryShown(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a", "1")
	r.Commit("one")
	r.Write("a", "2")
	r.Commit("two")
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	s.setGraphEvery(-1, 0) // no graph of the app's, which would load history again
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "refs", func() bool { return s.State.Refs != nil })
	s.LoadHistory()
	m.until(t, "history", func() bool { return s.History.Done })
	before := s.History

	r.Write("a", "3")
	r.Commit("three")
	was := s.State.Refs
	m.until(t, "the refs of the commit", func() bool { return s.State.Refs != was })
	s.LoadHistory()
	// It grew the history and handed it over; the main thread, busy, has
	// not run that yet.
	s.hist.loaded()

	// Another commit, whose refs the main thread takes first.
	r.Write("a", "4")
	r.Commit("four")
	rd := s.Repo.Refs()
	head, err := rd.Head()
	if err != nil {
		t.Fatal(err)
	}
	list, err := rd.List()
	if err != nil {
		t.Fatal(err)
	}
	s.State.Head, s.State.Refs = head, list
	s.LoadHistory()
	m.until(t, "the history of both commits", func() bool { return s.History.For == list && s.History.Done })
	h := s.History
	if h.Err != nil {
		t.Fatal(h.Err)
	}
	if h.Base != nil && h.Base != before.Snap {
		t.Fatalf("%d rows came over a history that was never shown", h.Added)
	}
	want := strings.Fields(r.Git("rev-list", "--topo-order", "HEAD"))
	if got := historyOIDs(h); !slices.Equal(got, want) {
		t.Fatalf("rows\n%v\nwant\n%v", got, want)
	}
}

// A history laid out anew comes with the rows of the commits the view
// keeps as it takes the history, not as the load looked for them: a
// commit chosen in between is found too, where its choice was dropped.
func TestHistoryFindsWhatIsKeptAsItIsTaken(t *testing.T) {
	r := gittest.Init(t)
	for _, c := range []string{"one", "two", "three"} {
		r.Write("a", c)
		r.Commit(c)
	}
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	s.setGraphEvery(-1, 0)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "refs", func() bool { return s.State.Refs != nil })
	s.LoadHistory()
	m.until(t, "history", func() bool { return s.History.Done })
	id := func(row int) []byte { return bytes.Clone(s.History.OID(s.History.Snap.Commit(row))) }
	top, chosen := id(0), id(2)

	s.KeepInView(top)
	s.historyLoadN++ // as the commit-graph written again has it loaded again
	s.LoadHistory()
	s.hist.loaded()
	// Chosen while the history waits for the main thread.
	s.KeepInView(chosen, top)
	m.until(t, "the history laid out anew", func() bool { return s.History.Load == s.historyLoadN && s.History.Done })
	h := s.History
	if h.Err != nil {
		t.Fatal(h.Err)
	}
	if row, ok := h.Found[string(chosen)]; !ok || row != 2 {
		t.Fatalf("the commit chosen meanwhile: row %d, looked for %v; found %d commits", row, ok, len(h.Found))
	}
	if row, ok := h.Found[string(top)]; !ok || row != 0 {
		t.Fatalf("the commit at the top: row %d, looked for %v", row, ok)
	}
}
