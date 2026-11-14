package session

import (
	"bytes"
	"encoding/hex"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/diff"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/history"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

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
	r.Write("a", "stashed")
	r.Git("stash", "push", "-q", "-m", "wip")

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
	h := s.History
	tag := strings.TrimSpace(r.Git("rev-parse", "v1"))
	tagged := strings.TrimSpace(r.Git("rev-parse", "v1^{commit}"))
	if c := h.TagCommits[string(mustHex(t, tag))]; hex.EncodeToString(c) != tagged {
		t.Fatalf("tag v1 names %x, want %s", c, tagged)
	}
	if len(h.Stashes) != 1 || h.Stashes[0].Message != "On main: wip" ||
		hex.EncodeToString(h.Stashes[0].Base) != strings.TrimSpace(r.Git("rev-parse", "HEAD")) {
		t.Fatalf("stashes %+v", h.Stashes)
	}
	r.Git("commit-graph", "write", "--reachable")
	load(true)

	root := mustHex(t, strings.TrimSpace(r.Git("rev-list", "--max-parents=0", "HEAD")))
	var rd *CommitDetail
	s.LoadCommit(root, func(cd CommitDetail) { rd = &cd })
	m.until(t, "the root commit", func() bool { return rd != nil && rd.Done })
	var pr *PatchResult
	s.CommitPatch(*rd, nil, 0, diff.AllWhitespace, Sides{}, func(res PatchResult) { pr = &res })
	m.until(t, "the root patch", func() bool { return pr != nil })
	if pr.Err != nil || len(pr.Parsed.Files) != 1 || pr.Parsed.Path(0) != "a" {
		t.Fatalf("root patch %+v", pr)
	}

	var d *CommitDetail
	head, _ := hex.DecodeString(strings.TrimSpace(r.Git("rev-parse", "HEAD")))
	s.LoadCommit(head, func(cd CommitDetail) { d = &cd })
	m.until(t, "the commit", func() bool { return d != nil && d.Done })
	if d.Err != nil || len(d.Parents) != 2 || string(d.Commit.Message) != "merge side\n" {
		t.Fatalf("detail %+v", d)
	}
	if d.Files.Len() != 1 || string(d.Files.Path(0)) != "b" {
		t.Fatalf("files from first parent: %d", d.Files.Len())
	}
}

// Hiding a branch, showing one alone and filtering each load history
// again with what git itself would list.
func TestHistoryView(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a", "1")
	r.Commit("one")
	r.Git("checkout", "-q", "-b", "side")
	r.Write("b", "2")
	r.Commit("side needle")
	r.Git("checkout", "-q", "main")
	r.Write("a", "3")
	r.Commit("main work")

	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "refs", func() bool { return s.State.Refs != nil })
	check := func(v HistoryView, args ...string) {
		t.Helper()
		s.SetHistoryView(v)
		s.LoadHistory()
		m.until(t, "history", func() bool { return s.History.Done && s.History.View == s.historyViewN })
		if s.History.Err != nil {
			t.Fatal(s.History.Err)
		}
		if s.History.Filtered != !v.Filter.IsZero() {
			t.Errorf("%+v: filtered %v", v, s.History.Filtered)
		}
		got := historyOIDs(s.History)
		want := strings.Fields(r.Git(append([]string{"rev-list", "--topo-order"}, args...)...))
		if !slices.Equal(got, want) {
			t.Errorf("%+v: rows %v, want %v", v, got, want)
		}
	}
	check(HistoryView{Hidden: []string{"refs/heads/side"}}, "HEAD")
	check(HistoryView{Only: []string{"refs/heads/side"}}, "side")
	check(HistoryView{Filter: history.Filter{Message: "NEEDLE"}}, "-i", "--grep=needle", "HEAD", "side")
	check(HistoryView{}, "HEAD", "side")
}

// A commit of 10,000 files hands over its message before its files, and
// a load cancelled for a newer selection hands over nothing more.
func TestLoadCommitParts(t *testing.T) {
	if testing.Short() {
		t.Skip("writes 10,000 files")
	}
	r := gittest.Init(t)
	r.Write("a", "1")
	r.Commit("one")
	for i := range 10000 {
		r.Write("many/"+strconv.Itoa(i%100)+"/f"+strconv.Itoa(i), "x\n")
	}
	big := mustHex(t, r.Commit("ten thousand files"))
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "refs", func() bool { return s.State.Refs != nil })

	var parts []CommitDetail
	start := time.Now()
	var first time.Duration
	s.LoadCommit(big, func(d CommitDetail) {
		if first == 0 {
			first = time.Since(start)
		}
		parts = append(parts, d)
	})
	m.until(t, "the commit", func() bool { return len(parts) > 0 && parts[len(parts)-1].Done })
	t.Logf("message after %v, files after %v", first.Round(time.Millisecond), time.Since(start).Round(time.Millisecond))
	if parts[0].Files != nil || string(parts[0].Commit.Message) != "ten thousand files\n" {
		t.Fatalf("first part %+v", parts[0])
	}
	last := parts[len(parts)-1]
	if last.Err != nil || last.Files == nil || last.Files.Len() != 10000 {
		t.Fatalf("last part err %v files %v", last.Err, last.Files)
	}
	for _, p := range parts {
		if !bytes.Equal(p.OID, big) {
			t.Fatalf("a part for %x", p.OID)
		}
	}

	// Read again, not handed over from what was kept.
	s.DropAnswers()
	stale := 0
	cancel := s.LoadCommit(big, func(CommitDetail) { stale++ })
	cancel()
	var other *CommitDetail
	s.LoadCommit(mustHex(t, strings.TrimSpace(r.Git("rev-parse", "HEAD~1"))), func(d CommitDetail) { other = &d })
	m.until(t, "the other commit", func() bool { return other != nil && other.Done })
	if stale != 0 {
		t.Fatalf("a cancelled load handed over %d parts", stale)
	}
}
