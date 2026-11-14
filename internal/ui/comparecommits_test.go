package ui

import (
	"strings"
	"testing"
)

// Any commit compares with HEAD from its menu: the files changed between
// them, and a file's patch from one to the other.
func TestCompareCommits(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "a1\n")
	r.Commit("add a")
	r.Write("a.txt", "a2\n")
	r.Write("b.txt", "b\n")
	r.Commit("change a, add b")
	r.Write("c.txt", "c\n")
	r.Commit("add c")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	h.click("History")
	h.until("history", h.shows("add a"))
	h.menu("add a", "Compare with HEAD")
	h.until("the comparison", h.shows("3 files changed"))
	tb := h.app.current()
	h.until("a's patch", patchHas(&tb.cmp.cv.pv, "a2", "a1"))
	if !strings.HasPrefix(tb.cmp.fromName, strings.TrimSpace(r.Git("rev-parse", "--short=10", "HEAD~2"))) {
		t.Fatalf("from %q", tb.cmp.fromName)
	}
	h.click("Swap")
	h.until("the other way", func() bool { return tb.cmp.fromName == "HEAD" && h.tt.HasText("3 files changed") })
	h.until("a's patch reversed", patchHas(&tb.cmp.cv.pv, "a1", "a2"))
}
