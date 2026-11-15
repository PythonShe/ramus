package ui

import (
	"testing"
)

// A commit's files show the lines each changes, and the list their sum.
func TestCommitLines(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "1\n2\n")
	r.Write("b.txt", "x\ny\nz\n")
	r.Commit("first")
	r.Write("a.txt", "1\ntwo\n3\n")
	r.Write("c.txt", "new\n")
	r.Git("rm", "-q", "b.txt")
	r.Commit("second")
	h.app.Open(r.Dir)
	h.app.current().sel = "history"
	h.until("the history", h.shows("second"))
	h.click("second")
	h.until("the files counted", func() bool {
		_, ok := h.tt.Access("modified a.txt, 2 lines added, 1 deleted")
		return ok
	})
	for _, name := range []string{"added c.txt, 1 line added, 0 deleted", "deleted b.txt, 0 lines added, 3 deleted"} {
		if _, ok := h.tt.Access(name); !ok {
			t.Errorf("no row reads %q", name)
		}
	}
	if !h.tt.HasText("+3") || !h.tt.HasText("−4") {
		t.Errorf("no sum in %q", h.tt.Texts())
	}
}
