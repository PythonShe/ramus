package ui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// Find puts the focus in the filter of the view shown; the tab keys go
// round the repositories open, and the menu is told as the window comes
// to show one.
func TestFindAndTabKeys(t *testing.T) {
	h := newHarness(t)
	var told []bool
	h.app.RepoCommandsChanged = func(on bool) { told = append(told, on) }
	one, two := h.repoWith("a.txt"), h.repoWith("b.txt")
	h.trust.Set(one.GitDir(), true)
	h.trust.Set(two.GitDir(), true)
	h.app.Open(one.Dir)
	h.until("the first", h.shows("a.txt"))
	if len(told) != 1 || !told[0] || !h.app.RepoCommandsOn() {
		t.Fatalf("told %v", told)
	}
	tb := h.app.current()
	tb.sel = "changes"
	h.tt.Frame()
	h.tt.Key(ui.Cmd, ui.KeyF)
	h.tt.Frame()
	h.tt.Frame()
	if !h.tt.Focused("Filter changes by path") {
		t.Fatal("Find did not focus the changes' filter")
	}
	h.app.Open(two.Dir)
	h.until("the second", h.shows("b.txt"))
	h.tt.Key(ui.Ctrl, ui.KeyTab)
	h.tt.Frame()
	if h.app.active != 0 {
		t.Fatalf("next tab: %d", h.app.active)
	}
	h.tt.Key(ui.Ctrl|ui.Shift, ui.KeyTab)
	h.tt.Frame()
	if h.app.active != 1 {
		t.Fatalf("previous tab: %d", h.app.active)
	}
	h.app.CloseAll()
	h.tt.Frame()
	if len(told) != 2 || told[1] {
		t.Fatalf("told %v", told)
	}
}
