package ui

import (
	"sync/atomic"
	"testing"

	"ramus/internal/issuelink"
)

// A commit's message is searched for issue references once for the
// commit and the rules, not at every frame that shows it; the rules
// changed, it is searched again.
func TestCommitMessageSearchedOnce(t *testing.T) {
	var searches atomic.Int32
	find := findLinks
	findLinks = func(set *issuelink.Set, text string) []issuelink.Match {
		searches.Add(1)
		return find(set, text)
	}
	t.Cleanup(func() { findLinks = find })

	h := newHarness(t)
	t.Cleanup(h.closeAll)
	r := h.repoWith()
	r.Git("remote", "add", "origin", "https://github.com/o/r.git")
	r.Write("a.txt", "alpha\n")
	r.Commit("fix the parser (#12)\n\nIt read past the end; see #34.")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("open", h.shows("No local changes"))
	tb := h.app.current()
	h.click("History")
	h.until("the commits", h.shows("fix the parser (#12)"))
	h.click("fix the parser (#12)")
	h.until("the links", func() bool {
		m := &tb.hist.detail.shown
		return !tb.hist.detail.loading && len(m.subjectRefs) == 1 && len(m.bodyRefs) == 1
	})
	n := searches.Load()
	frames := 0
	h.until("ten frames", func() bool { frames++; return frames > 10 })
	if got := searches.Load(); got != n {
		t.Fatalf("searched %d times in ten frames", got-n)
	}
	// Rules read again are another set: the message is searched anew.
	was := tb.links.set
	tb.links.For = nil
	h.until("searched with the new rules", func() bool { return tb.links.set != was && tb.hist.detail.shown.links == tb.links.set })
	if got := searches.Load(); got != n+2 {
		t.Fatalf("searched %d times with the new rules, want 2", got-n)
	}
}
