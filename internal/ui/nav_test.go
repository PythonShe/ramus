package ui

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestBackAndForward(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "alpha\n")
	r.Commit("add alpha")
	r.Write("README", "hello again\n")
	r.Commit("edit readme")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("open", h.shows("No changes"))
	tab := h.app.current()
	head := strings.TrimSpace(r.Git("rev-parse", "HEAD"))
	parent := strings.TrimSpace(r.Git("rev-parse", "HEAD~1"))
	chosen := func() string { return hex.EncodeToString(tab.hist.chosenID) }

	h.click("History")
	h.until("the commits", h.shows("edit readme"))
	h.click("edit readme")
	h.until("the detail", h.shows(parent))
	// A parent gone to is a page of its own.
	h.click(parent)
	h.until("the parent", func() bool { return chosen() == parent })

	h.click("Back")
	h.until("back to the child", func() bool { return chosen() == head && tab.sel == "history" })
	h.tt.Key(0, ui.KeyBack)
	h.until("back to the changes", func() bool { return tab.sel == sideChanges })
	h.tt.Key(0, ui.KeyForward)
	h.until("forward to the history", func() bool { return tab.sel == "history" && chosen() == head })
	h.click("Forward")
	h.until("forward to the parent", func() bool { return chosen() == parent })

	// Going somewhere else forgets the pages ahead.
	h.click("Back")
	h.until("back again", func() bool { return chosen() == head })
	h.click("Reflog")
	h.until("the reflog", func() bool { return tab.sel == "reflog" })
	if tab.nav.router.CanGoForward() {
		t.Fatal("pages ahead kept after going elsewhere")
	}
}

func TestSidebarToggle(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("open", h.shows("No changes"))
	h.click("Hide Sidebar")
	h.until("no sidebar", func() bool { return !h.tt.HasText("Repository sections") })
	if !h.app.Settings.NoSidebar {
		t.Fatal("the sidebar's absence is not kept")
	}
	h.click("Show Sidebar")
	h.until("the sidebar", h.shows("Repository sections"))

	// The panes beside the sidebar stay as it comes and goes: the focus
	// stays in the field it was in.
	h.click("History")
	h.until("the history", h.shows("Go to hash or ref"))
	field := "Go to hash or ref"
	h.click(field)
	if !h.tt.Focused(field) {
		t.Fatal("the field did not take the focus")
	}
	for range 2 {
		h.app.runCommand("toggle-sidebar")
		h.tt.Frame()
		if !h.tt.Focused(field) {
			t.Fatalf("the field lost the focus as the sidebar toggled (hidden %v)", h.app.Settings.NoSidebar)
		}
	}
}
