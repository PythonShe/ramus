package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"ramus/internal/git/commitgraph"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/run"
	"ramus/internal/git/write"
	"ramus/locales"
)

// graphWrites are the commit-graph writes in the log.
func graphWrites(s *Session) []*LogEntry {
	var out []*LogEntry
	for _, e := range s.Log {
		if e.Name == locales.SessionWriteCommitGraph() {
			out = append(out, e)
		}
	}
	return out
}

// History without a commit-graph writes one on its own, in the
// background, and loads again ordered from it; a write that fails, as
// when another git holds the lock, is tried again quietly, and none is
// told of as an error.
func TestHistoryWritesTheGraph(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a", "1")
	r.Commit("one")
	r.Write("a", "2")
	r.Commit("two")
	lock := filepath.Join(r.GitDir(), "objects", "info", "commit-graphs", "commit-graph-chain.lock")
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	s.setGraphEvery(time.Hour, 50*time.Millisecond)
	m.until(t, "refs", func() bool { return s.State.Refs != nil })
	s.LoadHistory()
	m.until(t, "history", func() bool { return s.History.Done })
	if s.History.Native || s.History.Advice == nil {
		t.Fatalf("native %v, advice %v, without a commit-graph", s.History.Native, s.History.Advice)
	}
	m.until(t, "two writes failed on the lock", func() bool {
		w := graphWrites(s)
		return len(w) >= 2 && !w[1].Running
	})
	for _, e := range graphWrites(s) {
		if !e.Quiet || !e.Running && e.Err == nil {
			t.Fatalf("a write with the lock held: quiet %v, err %v", e.Quiet, e.Err)
		}
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	m.until(t, "history from the graph", func() bool {
		s.LoadHistory()
		if s.History.Snap == nil || s.History.Snap.Len() != 2 {
			t.Fatalf("history went: %+v", s.History)
		}
		return s.History.Native && s.History.Done
	})
	w := graphWrites(s)
	if last := w[len(w)-1]; last.Err != nil || !last.Quiet || last.Running {
		t.Fatalf("the last write: %v", last.Err)
	}
	if s.History.Advice != nil {
		t.Fatalf("advice once written: %q", s.History.Advice.Cmd.Line())
	}
	// A layer of the chain, with changed-path filters, as the history of
	// a file wants.
	g, err := commitgraph.Open(filepath.Join(r.GitDir(), "objects"))
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if g.Bloom().Version == 0 || !g.HasGenerations() {
		t.Fatal("written without changed-path filters or generations")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(lock), "commit-graph-chain")); err != nil {
		t.Fatal(err)
	}
	r.Git("commit-graph", "verify")
}

// A write of the user's goes first: no quiet write is queued behind one,
// and one is tried again once it is done.
func TestGraphWriteGivesWay(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a", "1")
	r.Commit("one")
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	s.setGraphEvery(time.Hour, 50*time.Millisecond)
	m.until(t, "refs", func() bool { return s.State.Refs != nil })
	j, err := s.Submit(write.Op{Name: "Slow", Cmd: run.Cmd{GitDir: s.Repo.GitDir, WorkTree: r.Dir,
		Config: []string{"alias.slow=!sleep 1"}, Args: []string{"slow"}}})
	if err != nil {
		t.Fatal(err)
	}
	s.LoadHistory()
	m.until(t, "history", func() bool { return s.History.Done })
	m.until(t, "a retry", func() bool { return s.graph.retry })
	select {
	case <-j.Done():
		t.Skip("the user's write ended before history loaded")
	default:
	}
	if len(graphWrites(s)) != 0 {
		t.Fatal("a quiet write queued behind the user's")
	}
	m.until(t, "the graph", func() bool {
		s.LoadHistory()
		return s.History.Native && s.History.Done
	})
	if w := graphWrites(s); len(w) != 1 || !w[0].Quiet || w[0].Err != nil {
		t.Fatalf("writes %d", len(w))
	}
}
