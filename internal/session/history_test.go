package session

import (
	"encoding/hex"
	"slices"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/history"
)

func historyOIDs(h HistoryState) []string {
	var out []string
	for i := range h.Snap.Len() {
		out = append(out, hex.EncodeToString(h.OID(h.Snap.Commit(i))))
	}
	return out
}

func TestHistoryLoadsEveryTip(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a", "1")
	r.Commit("one")
	r.Git("checkout", "-q", "-b", "side")
	r.Write("b", "2")
	r.Commit("side work")
	// A commit only an annotated tag reaches, loose so it has no peeled
	// value in packed-refs.
	r.Git("checkout", "-q", "--detach")
	r.Write("c", "3")
	r.Commit("tagged only")
	r.Git("tag", "-a", "-m", "release", "v1")
	r.Git("checkout", "-q", "main")
	r.Write("a", "4")
	r.Commit("main work")
	r.Git("merge", "-q", "--no-ff", "-m", "merge side", "side")

	want := strings.Fields(r.Git("rev-list", "HEAD", "--branches", "--tags"))
	slices.Sort(want)

	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "refs", func() bool { return s.State.Refs != nil })

	load := func(native bool) {
		t.Helper()
		s.History, s.hist.loading = HistoryState{}, nil
		s.LoadHistory()
		m.until(t, "history", func() bool { return s.History.Done })
		h := s.History
		if h.Err != nil || h.Native != native {
			t.Fatalf("history err %v native %v", h.Err, h.Native)
		}
		got := historyOIDs(h)
		if got[0] != strings.TrimSpace(r.Git("rev-parse", "HEAD")) {
			t.Fatalf("first row %s is not HEAD", got[0])
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Fatalf("native %v: rows\n%v\nwant\n%v", native, got, want)
		}

		var c *history.TextChunk
		s.HistoryText(h, 0, h.Snap.Len(), func(tc *history.TextChunk, err error) {
			if err != nil {
				t.Error(err)
			}
			c = tc
		})
		m.until(t, "text", func() bool { return c != nil })
		if subject, _, _ := c.Row(0); subject != "merge side" {
			t.Fatalf("first subject %q", subject)
		}
	}
	load(false)
	r.Git("commit-graph", "write", "--reachable")
	load(true)

	var d *CommitDetail
	head, _ := hex.DecodeString(strings.TrimSpace(r.Git("rev-parse", "HEAD")))
	s.LoadCommit(head, func(cd CommitDetail) { d = &cd })
	m.until(t, "the commit", func() bool { return d != nil })
	if d.Err != nil || len(d.Parents) != 2 || string(d.Commit.Message) != "merge side\n" {
		t.Fatalf("detail %+v", d)
	}
	if d.Files.Len() != 1 || string(d.Files.Path(0)) != "b" {
		t.Fatalf("files from first parent: %d", d.Files.Len())
	}
}
