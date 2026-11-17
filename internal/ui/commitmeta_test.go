package ui

import (
	"strings"
	"testing"
)

// In a narrow commit panel a commit's hash stays on one line inside it,
// cut in its middle, rather than run past its edge or break over lines;
// its parent shows short.
func TestCommitHashesFit(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "1\n")
	r.Commit("second")
	h.app.Open(r.Dir)
	tb := h.app.current()
	tb.sel = "history"
	h.until("the history", h.shows("second"))
	h.click("second")
	head := strings.TrimSpace(r.Git("rev-parse", "HEAD"))
	parent := strings.TrimSpace(r.Git("rev-parse", "HEAD^"))
	h.until("the commit", h.shows(head))
	label, _ := h.tt.Find("Parent")
	b, ok := h.tt.Find(head)
	if !ok {
		t.Fatalf("no %s in %q", head, h.tt.Texts())
	}
	// The harness's window is 1200 wide; the panel keeps an inset.
	if b.X+b.W >= 1200 || b.H >= 1.5*label.H {
		t.Errorf("%s at %+v, a line %v high", head, b, label.H)
	}
	// A parent goes by its short id and its subject.
	h.until("the parent", func() bool { return h.tt.HasText(parent[:10]) && hasExact(h, "one") })
}
