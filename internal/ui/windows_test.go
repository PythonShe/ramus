package ui

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"

	"ramus/internal/library"
	"ramus/internal/settings"
)

// A tab moves to a window of its own, a worktree opens in one, and a
// setting changed in one window reaches the others.
func TestNewWindow(t *testing.T) {
	h := newHarness(t)
	one, two := h.repoWith(), h.repoWith()
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	other := filepath.Join(tmp, "wt")
	t.Cleanup(h.closeAll) // before the folder above goes
	one.Git("worktree", "add", "-q", other, "-b", "topic")
	var opened [][]string
	h.app.NewWindow = func(paths []string) { opened = append(opened, paths) }
	var told []settings.Settings
	h.app.SettingsChanged = func(s settings.Settings) { told = append(told, s) }
	h.trust.Set(one.GitDir(), true)
	h.app.Open(one.Dir)
	h.app.Open(two.Dir)
	h.until("both tabs", func() bool { return len(h.app.tabs) == 2 && h.tt.HasText("No changes") })
	if !h.app.Has(one.Dir) || h.app.Has(other) {
		t.Fatal("Has")
	}

	// From the keyboard: an empty window.
	h.tt.Key(ui.Cmd|ui.Shift, ui.KeyN)
	h.tt.Frame()
	if len(opened) != 1 || opened[0] != nil {
		t.Fatalf("opened %q", opened)
	}

	// The second tab, from its menu: it leaves this window.
	h.menu(filepath.Base(two.Dir), "Move to New Window")
	if len(opened) != 2 || !slices.Equal(opened[1], []string{library.Clean(two.Dir)}) || len(h.app.tabs) != 1 || h.app.Has(two.Dir) {
		t.Fatalf("opened %q, %d tabs", opened, len(h.app.tabs))
	}

	// A worktree, from the sidebar.
	h.app.active = 0
	label := "wt  topic"
	h.menu(label, "Open in New Window")
	if len(opened) != 3 || len(opened[2]) != 1 || library.Clean(opened[2][0]) != library.Clean(other) {
		t.Fatalf("opened %q", opened)
	}

	// A setting changed here is told; one changed elsewhere is used.
	h.app.setSidebar(false)
	if len(told) != 1 || !told[0].NoSidebar {
		t.Fatalf("told %+v", told)
	}
	s := h.app.Settings
	s.NoSidebar = false
	h.app.UseSettings(s)
	h.until("the sidebar back", h.shows("Repository sections"))
}
