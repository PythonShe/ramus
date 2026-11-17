package ui

import (
	"strings"
	"testing"
)

// A file a commit changed has a menu: its path copied, its history.
func TestCommitFileMenu(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "alpha\n")
	r.Commit("add alpha")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	tb := h.app.current()
	h.click("History")
	h.until("the history", h.shows("add alpha"))
	h.click("add alpha")
	h.until("the detail", h.shows("1 file changed"))
	h.menu("a.txt", "Copy Path")
	if got := h.tt.Clipboard(); got != "a.txt" {
		t.Fatalf("copied %q", got)
	}
	h.menu("a.txt", "Show File History")
	h.until("the file's history", func() bool { return tb.sel == "file" && tb.file.path == "a.txt" })
}

// Narrow, a commit's labels go above what they label, which keeps the
// whole width: beside them, the hash and the date would break apart.
func TestCommitDetailNarrow(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "alpha\n")
	r.Commit("add alpha")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	tb := h.app.current()
	h.click("History")
	h.until("the history", h.shows("add alpha"))
	h.click("add alpha")
	h.until("the detail", h.shows("1 file changed"))
	parent := strings.TrimSpace(r.Git("rev-parse", "--short=10", "HEAD~1"))
	beside := func() bool {
		h.settle()
		l, _ := h.tt.Find("Parent")
		v, _ := h.tt.Find(parent)
		return v.Y < l.Y+l.H/2
	}
	if !beside() {
		t.Fatal("the parent under its label in a wide pane")
	}
	tb.hist.detail.file = 0
	h.until("the patch", func() bool { return tb.hist.detail.patchOpen() })
	tb.hist.detail.infoW = leastInfoW
	if beside() {
		t.Fatalf("the parent beside its label at %v wide", tb.hist.detail.metaW)
	}
}
