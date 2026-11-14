package ui

import (
	"fmt"
	"strings"
	"testing"
)

// A large patch shows its first page, then the rest without asking; past
// the shown limit it offers the whole patch, which then stages by line.
func TestPatchPages(t *testing.T) {
	first, shown := firstPatchPage, shownPatch
	t.Cleanup(func() { firstPatchPage, shownPatch = first, shown })
	firstPatchPage, shownPatch = 4<<10, 64<<10

	h := newHarness(t)
	r := h.repoWith()
	lines := func(prefix string, n int) string {
		var b strings.Builder
		for i := range n {
			fmt.Fprintf(&b, "%s line %05d of a file long enough to page\n", prefix, i)
		}
		return b.String()
	}
	r.Write("mid.txt", lines("old", 500))  // about 25 KB each side
	r.Write("big.txt", lines("old", 2000)) // about 100 KB each side
	r.Commit("files")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	r.Write("mid.txt", lines("new", 500))
	r.Write("big.txt", lines("new", 2000))
	h.until("the changes", func() bool { return h.tt.HasText("mid.txt") && h.tt.HasText("big.txt") })
	d := &h.app.current().cv.diff

	// Past the first page, under the shown limit: the rest comes alone.
	h.click("mid.txt")
	h.until("the rest of the patch", func() bool { return !d.loading && d.res.Parsed != nil })
	if cut(d.res) || d.pager.stage != pageRest || !d.partialOK() {
		t.Fatalf("mid.txt: cut %v, stage %d, stageable %v", cut(d.res), d.pager.stage, d.partialOK())
	}

	// Past the shown limit: cut, with the whole a click away.
	h.click("big.txt")
	h.until("the shown part", func() bool { return !d.loading && d.res.Parsed != nil && d.key.path == "big.txt" })
	if !cut(d.res) || d.partialOK() || !h.tt.HasText("Only the first 64 KB of this patch are shown; lines cannot be staged from it") {
		t.Fatalf("big.txt: cut %v, stageable %v; texts %q", cut(d.res), d.partialOK(), h.tt.Texts())
	}
	h.click("Load Whole Patch")
	h.until("the whole patch", func() bool { return !d.loading && !cut(d.res) && d.res.Parsed != nil })
	if !d.partialOK() || !d.res.HasSides || h.tt.HasText("Load Whole Patch") {
		t.Fatalf("whole: stageable %v, sides %v", d.partialOK(), d.res.HasSides)
	}
}
