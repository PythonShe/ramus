package ui

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"

	"ramus/internal/settings"
)

// Saves made back to back, as letting go of a history column makes two,
// reach the file in order: the last choice is the one kept.
func TestSettingsSavedInOrder(t *testing.T) {
	h := newHarness(t)
	h.app.SettingsPath = filepath.Join(t.TempDir(), "settings.json")
	for i := range 50 {
		h.app.setSidebar(i%2 == 1)
		h.app.keepMessage(float32(100+i), 1, 0)
	}
	h.app.keeper.Wait()
	s, err := settings.Load(h.app.SettingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if s.NoSidebar || s.HistoryMessage != 149 {
		t.Fatalf("file has the sidebar hidden %v, the message at %v", s.NoSidebar, s.HistoryMessage)
	}
}

// Every item and section of the sidebar, and the sidebar itself, can be
// left out, and brought back from the palette; the settings keep it.
func TestHideSidebarParts(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Git("tag", "v1")
	r.Git("branch", "topic")
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
	if err := h.tt.RightClick("topic"); err != nil {
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
}

// A section of refs with none is left out, and comes when one is made.
func TestEmptySectionsLeftOut(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the branches", h.shows("Branches (1)"))
	if h.tt.HasText("Remotes (0)") || h.tt.HasText("Tags (0)") {
		t.Fatalf("empty sections shown: %q", h.tt.Texts())
	}
	r.Git("tag", "v1")
	h.until("the tags", h.shows("Tags (1)"))
}

// A refresh that failed says so in a bar of its own, gone once all is
// well again.
func TestWarningBar(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	tb := h.app.current()
	tb.s.State.RefreshErr = errors.New("a test failure")
	h.until("the warning", h.shows("Refresh failed: a test failure"))
	tb.s.State.RefreshErr = nil
	h.until("no warning", func() bool { return !h.tt.HasText("Refresh failed: a test failure") })
}
