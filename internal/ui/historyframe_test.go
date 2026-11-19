package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ramus/internal/library"
	"ramus/internal/session"
)

// longHistory opens a repository of n commits, with a commit-graph, on
// History, and returns its tab once the whole history is laid out and
// what moved as it came is at rest.
func longHistory(t *testing.T, h *harness, n int) *tab {
	t.Helper()
	r := h.repoWith()
	var stream strings.Builder
	for i := range n {
		msg := fmt.Sprintf("commit %d\n", i)
		fmt.Fprintf(&stream, "commit refs/heads/main\ncommitter A U Thor <a@example.com> %d +0000\ndata %d\n%s", 1700000000+i, len(msg), msg)
		if i == 0 {
			stream.WriteString("from refs/heads/main^0\n")
		}
		fmt.Fprintf(&stream, "M 644 inline f.txt\ndata %d\n%d\n\n", len(fmt.Sprint(i))+1, i)
	}
	if out, err := r.Run([]byte(stream.String()), "fast-import", "--quiet"); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	r.Git("reset", "-q", "--hard")
	r.Git("commit-graph", "write", "--reachable")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()
	h.click("History")
	h.until("the whole history", func() bool {
		return tb.s.History.Done && tb.s.History.Snap != nil && tb.s.History.Snap.Len() == n+1
	})
	h.tt.Advance(time.Second)
	return tb
}

// A frame of the history list costs what it shows, however long the
// history: scrolling down a long one, each frame reads the commits of
// about the rows it shows, never of every commit, and once still the
// list asks for no more frames. Counted rather than timed, to hold on a
// loaded machine.
func TestHistoryFrameCostsWhatItShows(t *testing.T) {
	const n = 30000
	h := newHarness(t)
	tb := longHistory(t, h, n)
	hv := &tb.hist
	if h.tt.FramePending() {
		t.Fatal("history at rest still asks for frames")
	}
	// Every commit a frame reads goes through OID; it is called on the
	// main thread here, and atomically counted for any other.
	var reads atomic.Int64
	oid := tb.s.History.OID
	tb.s.History.OID = func(id uint32) []byte { reads.Add(1); return oid(id) }
	at, ok := h.tt.Find("Commits")
	if !ok {
		t.Fatal("no list of commits")
	}
	first, _ := hv.list.Visible()
	var most int64
	for range 40 {
		reads.Store(0)
		b0 := hv.frame
		h.tt.Wheel(at.X+at.W/2, at.Y+at.H/2, 0, 2400)
		h.tt.Advance(time.Second / 60)
		for len(h.updates) > 0 {
			(<-h.updates)()
		}
		// Frames that only moved the rows count as one.
		built := max(1, hv.frame-b0)
		most = max(most, reads.Load()/int64(built))
	}
	if now, _ := hv.list.Visible(); now <= first {
		t.Fatalf("the list did not scroll: it shows row %d, from %d", now, first)
	}
	shown := func() int { f, l := hv.list.Visible(); return l - f + 1 }()
	t.Logf("%d rows shown; at most %d commits read a frame, of %d", shown, most, n+1)
	// The rows shown, each read a few times, and the list looking a
	// little around its place; nothing near the history's length.
	if limit := int64(n / 10); most > limit {
		t.Fatalf("a frame read %d commits of a history of %d (rows shown %d), over %d: it does work for every commit", most, n+1, shown, limit)
	}
	h.tt.Advance(time.Second)
	if h.tt.FramePending() {
		t.Fatal("the list at rest after scrolling still asks for frames")
	}
}

// Text read of a repository that opened again under the read is not a
// failure to tell: the rows ask for it again of the repository open now.
// Any other failure is told.
func TestHistoryTextOfAReopenedRepositoryIsNotTold(t *testing.T) {
	h := newHarness(t)
	tb := longHistory(t, h, 300)
	hv := &tb.hist
	var ids [][]byte
	for r := range 3 {
		ids = append(ids, append([]byte(nil), tb.s.History.OID(tb.s.History.Snap.Commit(r))...))
	}
	hv.asked[0] = true
	h.app.dropNotice()
	h.app.gotText(tb, hv.text.gen, ids, nil, 0, session.ErrReopened)
	if h.app.notice != "" || tb.note.text != "" {
		t.Fatalf("told %q", h.app.notice)
	}
	if hv.asked[0] {
		t.Fatal("the rows whose text was cut off are not asked for again")
	}
	h.app.gotText(tb, hv.text.gen, ids, nil, 0, fmt.Errorf("cat-file died"))
	if h.app.notice == "" {
		t.Fatal("a failed read is not told")
	}
}

// A repository opened by another spelling of its path, here a symbolic
// link, takes the switches the manager sets by the library's path, the
// resolved one: its open tab stops fetching in the background, and opens
// files in the editor chosen, as the library says.
func TestRepositorySwitchesReachATabOpenByAnotherPath(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	link := filepath.Join(tmp, "link")
	if err := os.Symlink(r.Dir, link); err != nil {
		t.Skip(err)
	}
	h.trust.Set(r.GitDir(), true)
	h.app.Open(link)
	tb := h.app.current()
	h.until("the path resolved", func() bool { return tb.real != "" && tb.prefs.loaded })
	if tb.s.Path == tb.real {
		t.Fatalf("the tab is open on %s, its path resolved", tb.s.Path)
	}
	h.app.setFetchOff(library.Clean(link), true)
	if !tb.prefs.fetchOff {
		t.Fatal("the open tab still fetches after the manager turned it off")
	}
	h.app.setEditor(library.Clean(link), "vim")
	if tb.prefs.editor != "vim" {
		t.Fatalf("the open tab's editor is %q", tb.prefs.editor)
	}
}
