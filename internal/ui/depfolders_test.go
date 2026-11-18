package ui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// A dependency folder git does not track is left out of the changes
// unless the setting shows it; one beside it, and a new folder holding
// one, are listed.
func TestDependencyFoldersHidden(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a", "1")
	r.Commit("one")
	r.Write("node_modules/left/index.js", "x")
	r.Write("pkg/new/node_modules/b.js", "x")
	r.Write("pkg/new/index.js", "x")
	r.Write("notes.md", "x")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the changes", h.shows("notes.md"))
	if !h.tt.HasText("pkg/") || h.tt.HasText("node_modules/") {
		t.Fatalf("texts %q", h.tt.Texts())
	}

	h.app.openSettings()
	h.tt.Frame()
	sw, ok := h.tt.AccessByRole(ui.RoleSwitch, "Hide dependency folders git does not track")
	if !ok || !sw.Checked {
		t.Fatalf("the switch: %+v %v", sw, ok)
	}
	h.tt.ClickAt(sw.Bounds.X+sw.Bounds.W/2, sw.Bounds.Y+sw.Bounds.H/2)
	h.tt.Frame()
	if !h.app.Settings.ShowDependencyFolders {
		t.Fatal("the switch changed no setting")
	}
	h.app.settingsUI.open = false
	h.until("shown", h.shows("node_modules/"))
}
