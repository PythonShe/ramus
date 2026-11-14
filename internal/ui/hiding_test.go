package ui

import (
	"errors"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
)

// Every item and section of the sidebar, and the sidebar itself, can be
// left out, and brought back from the palette; the settings keep it.
func TestHideSidebarParts(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Git("tag", "v1")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	palette := func(text string) {
		t.Helper()
		h.app.palette.open = true
		h.tt.Frame()
		h.tt.Type(text)
		h.tt.Frame()
		h.tt.Key(0, ui.KeyEnter)
		h.tt.Frame()
	}

	h.menu("Reflog", "Hide Reflog from the Sidebar")
	h.until("reflog gone", func() bool { return !hasExact(h, "Reflog") })
	h.menu("Tags (1)", "Hide Tags from the Sidebar")
	h.until("tags gone", func() bool { return !hasExact(h, "Tags (1)") && !hasExact(h, "v1") })
	if !slices.Equal(h.app.Settings.HiddenSections, []string{"reflog", "tags"}) {
		t.Fatalf("kept %q", h.app.Settings.HiddenSections)
	}
	// A branch in a section has its own menu.
	tb := h.app.current()
	h.until("the refs", func() bool { return tb.refs.info != nil })
	if err := h.tt.RightClick("main"); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()
	if menu := h.tt.Menu(); slices.Contains(menu, "Hide Branches from the Sidebar") || len(menu) == 0 {
		t.Fatalf("the branch's menu: %q", menu)
	}
	h.tt.CloseMenu()

	palette("show reflog in")
	h.until("reflog back", func() bool { return hasExact(h, "Reflog") })
	palette("show tags in")
	h.until("tags back", func() bool { return hasExact(h, "Tags (1)") })

	palette("hide sidebar")
	h.until("no sidebar", func() bool { return !hasExact(h, "History") && h.app.Settings.NoSidebar })
	palette("show sidebar")
	h.until("the sidebar", func() bool { return hasExact(h, "History") })

	// The status bar names the branch; hidden, it comes back for a
	// warning.
	count := func() int {
		n := 0
		for _, s := range h.tt.Texts() {
			if s == "main" {
				n++
			}
		}
		return n
	}
	with := count()
	palette("hide status bar")
	h.until("no status bar", func() bool { return count() == with-1 })
	tb.s.State.RefreshErr = errors.New("a test failure")
	h.until("the warning", h.shows("Refresh failed: a test failure"))
	tb.s.State.RefreshErr = nil
	palette("show status bar")
	h.until("the status bar", func() bool { return count() == with })
}
