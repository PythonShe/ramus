package ui

import (
	"testing"

	"ramus/internal/git/repo"
	"ramus/internal/session"
)

// The patch of a commit's file, dropped as another commit is chosen, is
// marked stale, so that the decoding of its pictures, which waits its
// turn, does not run once the view has gone.
func TestCommitViewDropsItsPatch(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "alpha\n")
	r.Commit("add alpha")
	r.Write("README", "hello again\n")
	r.Commit("edit readme")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("open", h.shows("No local changes"))
	tb := h.app.current()
	h.click("History")
	// Both rows: the newest commit's subject shows before the list does.
	h.until("the commits", func() bool { return h.shows("edit readme")() && h.shows("add alpha")() })
	h.click("add alpha")
	h.until("the detail", h.shows("1 file changed"))
	h.click("a.txt")
	h.until("its patch", patchHas(&tb.hist.detail.pv, "alpha", ""))
	stale := tb.hist.detail.pv.stale
	if stale == nil || stale.Load() {
		t.Fatalf("the patch shown is stale: %v", stale)
	}
	h.click("edit readme")
	h.until("the other commit", func() bool { return stale.Load() })
}

// The worktree panel marks the patch it shows stale as it shows another
// file, goes back to the list, or stops.
func TestWorktreePanelDropsItsPatch(t *testing.T) {
	h := newHarness(t)
	t.Cleanup(h.closeAll)
	r := h.repoWith("a.txt")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("open", func() bool { c := h.app.current(); return c != nil && c.s.Repo != nil })
	tb := h.app.current()
	p := &tb.wtp
	for _, drop := range []func(){
		func() { p.show(h.app, tb, repo.Worktree{Path: r.Dir}, session.WorktreeFile{Path: "a.txt"}) },
		p.halt,
	} {
		stale := newStaleness()
		p.pv.stale = stale
		drop()
		if !stale.Load() {
			t.Fatal("the patch dropped is not stale")
		}
	}
}
