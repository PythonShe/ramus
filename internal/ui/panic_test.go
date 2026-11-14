package ui

import (
	"context"
	"strings"
	"testing"
)

// A panic in one repository's work is shown in its tab; the other tab
// and the window go on, and Reopen brings the repository back.
func TestPanicInTab(t *testing.T) {
	h := newHarness(t)
	h.panics = true
	one, two := h.repoWith("a.txt"), h.repoWith("b.txt")
	h.trust.Set(one.GitDir(), true)
	h.trust.Set(two.GitDir(), true)
	h.app.Open(one.Dir)
	h.until("the first", h.shows("a.txt"))
	h.app.Open(two.Dir)
	h.until("the second", h.shows("b.txt"))
	h.app.active = 0
	broken := h.app.current()
	broken.sel = "history"
	broken.s.Repo.Life().Go(func(context.Context) { panic("index out of range [3] with length 3") })
	h.until("the banner", h.shows("Something in Ramus failed while working on this repository: index out of range [3] with length 3"))
	h.click("Copy Details")
	h.until("the details copied", func() bool { return strings.Contains(h.tt.Clipboard(), "panic_test.go") })
	h.app.active = 1
	h.until("the other tab", h.shows("b.txt"))
	if h.tt.HasText("Something in Ramus failed") {
		t.Fatal("the other tab shows the failure")
	}
	h.app.active = 0
	h.tt.Frame()
	h.click("Reopen")
	h.until("the repository again", func() bool { return h.app.current().s.Repo != nil && h.app.current().s.State.Status != nil })
	if tb := h.app.current(); tb == broken || tb.s.Panic != nil || tb.sel != "history" || h.app.active != 0 || tb.s.Path != broken.s.Path {
		t.Fatalf("reopened %+v at %d", tb.s.Path, h.app.active)
	}
	h.until("no banner", func() bool { return !h.tt.HasText("Something in Ramus failed") })
}
