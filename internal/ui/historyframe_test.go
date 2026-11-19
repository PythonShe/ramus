package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

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
