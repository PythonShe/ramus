package ui

import (
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"

	"ramus/internal/settings"
)

// A column of the branches view the user widens keeps its width in the
// settings file, which a window opened later starts from; the header's
// menu puts it back.
func TestColumnsKept(t *testing.T) {
	h := newHarness(t)
	h.app.SettingsPath = filepath.Join(t.TempDir(), "settings.json")
	r := h.repoWith()
	r.Git("branch", "other")
	r.Git("branch", "spare")
	r.Git("tag", "v1")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	h.click("Branches and Tags")
	h.app.setSidebar(false)
	h.until("the branches", func() bool { return hasExact(h, "other") && hasExact(h, "spare") })
	header := func(h *harness, title string) ui.Rect {
		t.Helper()
		info, ok := h.tt.AccessByRole(ui.RoleColumnHeader, title)
		if !ok {
			t.Fatalf("no header %q", title)
		}
		return info.Bounds
	}

	// Choosing a branch shows Switch in its row, which keeps its height.
	below, _ := h.tt.Find("spare")
	h.click("other")
	h.until("the Switch button", h.shows("Switch"))
	if now, _ := h.tt.Find("spare"); now.Y != below.Y {
		t.Fatalf("the row below moved from %v to %v", below.Y, now.Y)
	}

	// Dragging the right edge of Upstream's header 40 DIPs widens it.
	up := header(h, "Upstream")
	edge, y := up.X+up.W-2, up.Y+up.H/2
	h.tt.Press(edge, y)
	h.tt.Move(edge+20, y)
	h.tt.Move(edge+40, y)
	h.tt.Release(edge+40, y)
	h.tt.Frame()
	want := up.W + 40
	if w := h.app.Settings.Tables["branches"].Widths["upstream"]; w != want {
		t.Fatalf("Upstream kept at %v, want %v: %+v", w, want, h.app.Settings.Tables)
	}
	var saved settings.Settings
	h.until("the file", func() bool {
		s, err := settings.Load(h.app.SettingsPath)
		saved = s
		return err == nil && s.Tables["branches"].Widths["upstream"] == want
	})

	// A new window, with the settings as the file has them.
	h2 := newHarness(t)
	t.Cleanup(h2.closeAll) // before the repository's folder goes
	h2.app.Settings = saved
	h2.trust.Set(r.GitDir(), true)
	h2.app.Open(r.Dir)
	h2.until("the repository", h2.shows("No changes"))
	h2.app.current().sel = "refs"
	h2.until("the branches", func() bool { return hasExact(h2, "other") })
	if w := header(h2, "Upstream").W; w != want {
		t.Fatalf("Upstream restored at %v, want %v", w, want)
	}
	// The tags have a layout of their own.
	h2.click("Tags")
	h2.until("the tags", h2.shows("Kind"))
	if w := header(h2, "Kind").W; w != 100 {
		t.Fatalf("Kind at %v", w)
	}

	// Reset Columns, in the header's menu, forgets it.
	h2.click("Branches")
	h2.until("the branches again", h2.shows("Upstream"))
	if err := h2.tt.RightClick("Upstream"); err != nil {
		t.Fatal(err)
	}
	if err := h2.tt.ChooseMenuItem("Reset Columns"); err != nil {
		t.Fatalf("%v: %q", err, h2.tt.Menu())
	}
	h2.tt.Frame()
	if w := header(h2, "Upstream").W; w != up.W || h2.app.Settings.Tables != nil {
		t.Fatalf("reset to %v, want %v; kept %+v", w, up.W, h2.app.Settings.Tables)
	}
}
