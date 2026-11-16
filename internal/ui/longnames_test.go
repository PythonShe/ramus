package ui

import (
	"strings"
	"testing"
	"time"
)

// countExact is how many texts of the last frame are s.
func countExact(h *harness, s string) int {
	n := 0
	for _, x := range h.tt.Texts() {
		if x == s {
			n++
		}
	}
	return n
}

// A branch name too long for the sidebar or the branch list shows whole
// where the pointer rests on it.
func TestLongBranchNameShowsWhole(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	leaf := strings.Repeat("a-long-branch-name-", 4) + "end"
	long := "feature/" + leaf
	r.Git("branch", long)
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	h.until("the branch", func() bool { return hasExact(h, long) })

	// rest puts the pointer on the text s and waits out the tooltip's
	// delay.
	rest := func(s string) {
		t.Helper()
		h.tt.Move(0, 0)
		h.tt.Frame()
		at, ok := h.tt.Find(s)
		if !ok {
			t.Fatalf("no %q; texts %q", s, h.tt.Texts())
		}
		// Near its start: a table row's label spans the row.
		h.tt.Move(at.X+min(at.W/2, 60), at.Y+at.H/2)
		h.tt.Frame()
		h.tt.Advance(2 * time.Second)
		h.tt.Frame()
	}
	rest(long)
	if countExact(h, long) < 2 {
		t.Fatalf("the sidebar's name not shown whole; texts %q", h.tt.Texts())
	}

	h.click("Branches and Tags")
	h.until("the branch list", func() bool { return hasExact(h, leaf) })
	rest(leaf)
	if countExact(h, long) < 2 {
		t.Fatalf("the list's name not shown whole; texts %q", h.tt.Texts())
	}
}
