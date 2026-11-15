package session

import (
	"bytes"
	"encoding/hex"
	"fmt"
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
		s.History, s.hist.loading, s.hist.last = HistoryState{}, histKey{}, nil
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

// History is ordered natively past a commit-graph that lacks the newest
// commits, merges among them included, exactly as git orders it.
func TestHistoryPastTheGraph(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a", "1")
	r.Commit("one")
	r.Git("checkout", "-q", "-b", "side")
	r.Write("b", "1")
	r.Commit("side one")
	r.Git("checkout", "-q", "main")
	r.Write("a", "2")
	r.Commit("two")
	r.Git("commit-graph", "write", "--reachable", "--split")
	r.Git("checkout", "-q", "-b", "topic", "main~1")
	r.Write("c", "1")
	r.Commit("topic one")
	r.Git("checkout", "-q", "main")
	r.Write("a", "3")
	r.Commit("three")
	r.Env = []string{"GIT_COMMITTER_DATE=@" + strconv.FormatInt(r.Tick(), 10) + " +0000"}
	r.Git("merge", "-q", "--no-ff", "-m", "merge topic and side", "topic", "side")
	r.Env = nil

	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "refs", func() bool { return s.State.Refs != nil })
	s.LoadHistory()
	m.until(t, "history", func() bool { return s.History.Done })
	if s.History.Err != nil || !s.History.Native {
		t.Fatalf("history err %v native %v", s.History.Err, s.History.Native)
	}
	want := strings.Fields(r.Git("rev-list", "--topo-order", "HEAD", "--branches"))
	if got := historyOIDs(s.History); !slices.Equal(got, want) {
		t.Fatalf("rows\n%v\nwant\n%v", got, want)
	}
}

// shown pumps the main thread until history is loaded whole for the refs
// as they are, checking every state published on the way: never empty,
// never partial, as the history before stays until the new one is whole.
func shown(t *testing.T, m mainThread, s *Session, what string) HistoryState {
	t.Helper()
	before := s.State.Refs
	m.until(t, what+": the refs", func() bool { return s.State.Refs != before })
	m.until(t, what, func() bool {
		s.LoadHistory()
		h := s.History
		if h.Snap == nil || h.Snap.Len() == 0 || !h.Done || h.Err != nil {
			t.Fatalf("%s: history published empty, partial or failed: %+v", what, h)
		}
		return h.For == s.State.Refs && h.Load == s.historyLoadN
	})
	return s.History
}

func (h *hist) walked() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.walks
}

// A commit on top, and a fetch of 100 commits on top, are laid out alone
// above the rows there were, which stay as they were: no walk of the
// whole history, with the commit-graph or without one.
func TestHistoryGrowsOnTop(t *testing.T) {
	for _, graph := range []bool{true, false} {
		t.Run(fmt.Sprintf("graph=%v", graph), func(t *testing.T) {
			r := gittest.Init(t)
			r.Write("a", "1")
			r.Commit("one")
			r.Git("checkout", "-q", "-b", "side")
			r.Write("b", "1")
			r.Commit("side")
			r.Git("checkout", "-q", "main")
			r.Write("a", "2")
			r.Commit("two")
			r.Git("tag", "-a", "-m", "t", "v1")
			if graph {
				r.Git("commit-graph", "write", "--reachable", "--split")
			}
			m := make(mainThread, 64)
			s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
			t.Cleanup(func() { s.Close(); <-s.Done() })
			m.until(t, "refs", func() bool { return s.State.Refs != nil })
			s.LoadHistory()
			m.until(t, "history", func() bool { return s.History.Done })
			if s.History.Native != graph {
				t.Fatalf("native %v", s.History.Native)
			}
			want := func() []string {
				return strings.Fields(r.Git("rev-list", "--topo-order", "HEAD", "--branches", "--tags", "--remotes"))
			}
			walks := s.hist.walked()
			before := s.History
			r.Write("a", "3")
			r.Commit("three")
			h := shown(t, m, s, "the commit on top")
			if h.Base != before.Snap || h.Added != 1 || s.hist.walked() != walks {
				t.Fatalf("a commit on top: base %v added %d walks %d, was %d", h.Base == before.Snap, h.Added, s.hist.walked(), walks)
			}
			if got := historyOIDs(h); !slices.Equal(got, want()) {
				t.Fatalf("rows\n%v\nwant\n%v", got, want())
			}

			// A fetch: 100 commits on a remote branch, on top of the
			// newest commit, with a merge of a branch made among them.
			tip := strings.TrimSpace(r.Git("rev-parse", "HEAD"))
			var b strings.Builder
			when := r.Tick()
			for i := range 100 {
				when += 60
				fmt.Fprintf(&b, "commit refs/remotes/origin/main\nmark :%d\ncommitter F <f@example.com> %d +0000\ndata 7\nfetched\n", i+1, when)
				switch {
				case i == 0:
					fmt.Fprintf(&b, "from %s\n", tip)
				case i == 60:
					fmt.Fprintf(&b, "from :%d\nmerge :%d\n", i, i-10)
				case i == 50:
					fmt.Fprintf(&b, "from :%d\n", i-10)
				default:
					fmt.Fprintf(&b, "from :%d\n", i)
				}
				b.WriteString("\n")
			}
			r.Run([]byte(b.String()), "fast-import", "--quiet")
			before = h
			h = shown(t, m, s, "the fetch")
			if h.Base != before.Snap || h.Added != 100 || s.hist.walked() != walks {
				t.Fatalf("a fetch on top: base %v added %d walks %d, was %d", h.Base == before.Snap, h.Added, s.hist.walked(), walks)
			}
			if got := historyOIDs(h); !slices.Equal(got, want()) {
				t.Fatalf("rows\n%v\nwant\n%v", got, want())
			}
		})
	}
}

// History rewritten (an amend, a reset, a rebase) is laid out anew off
// the main thread: the history before stays shown until the new one is
// whole, which then finds the rows of the commits the view keeps.
func TestHistoryRewritten(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a", "1")
	one := r.Commit("one")
	r.Git("checkout", "-q", "-b", "side")
	r.Write("b", "1")
	r.Commit("side")
	r.Git("checkout", "-q", "main")
	for i := range 5 {
		r.Write("a", strconv.Itoa(i))
		r.Commit("main " + strconv.Itoa(i))
	}
	r.Git("commit-graph", "write", "--reachable", "--split")
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "refs", func() bool { return s.State.Refs != nil })
	s.LoadHistory()
	m.until(t, "history", func() bool { return s.History.Done })
	want := func() []string { return strings.Fields(r.Git("rev-list", "--topo-order", "HEAD", "--branches")) }
	kept := mustHex(t, strings.TrimSpace(r.Git("rev-parse", "main~3")))
	s.KeepInView(kept, mustHex(t, one))
	for _, step := range []struct {
		what   string
		change func()
	}{
		{"an amend", func() { r.Git("commit", "-q", "--amend", "-m", "amended") }},
		{"a reset", func() { r.Git("reset", "-q", "--hard", "HEAD~1") }},
		{"a commit on an older branch", func() {
			r.Git("checkout", "-q", "side")
			r.Write("b", "2")
			r.Commit("side again")
			r.Git("checkout", "-q", "main")
		}},
		{"a rebase", func() { r.Git("rebase", "-q", "side") }},
	} {
		walks := s.hist.walked()
		step.change()
		h := shown(t, m, s, step.what)
		if h.Base != nil || s.hist.walked() != walks+1 {
			t.Fatalf("%s: laid on top of the history before (%v), walks %d, was %d", step.what, h.Base != nil, s.hist.walked(), walks)
		}
		got := historyOIDs(h)
		if !slices.Equal(got, want()) {
			t.Fatalf("%s: rows\n%v\nwant\n%v", step.what, got, want())
		}
		for _, id := range [][]byte{kept, mustHex(t, one)} {
			row, ok := h.Found[string(id)]
			if wantRow := slices.Index(got, hex.EncodeToString(id)); !ok || row != wantRow {
				t.Fatalf("%s: %x found at %d (%v), it is at %d", step.what, id, row, ok, wantRow)
			}
		}
	}
}

// A commit-graph written by git loads history again, ordered from it.
func TestHistoryTakesANewGraph(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a", "1")
	r.Commit("one")
	r.Write("a", "2")
	r.Commit("two")
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "refs", func() bool { return s.State.Refs != nil })
	s.LoadHistory()
	m.until(t, "history", func() bool { return s.History.Done })
	if s.History.Native {
		t.Fatal("native without a commit-graph")
	}
	r.Git("commit-graph", "write", "--reachable", "--split")
	h := s.History
	m.until(t, "history from the graph", func() bool {
		s.LoadHistory()
		if s.History.Snap == nil || s.History.Snap.Len() != 2 {
			t.Fatalf("history went: %+v", s.History)
		}
		return s.History.Native && s.History.Done
	})
	if s.History.Load == h.Load {
		t.Fatal("loaded again without the graph changing")
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
