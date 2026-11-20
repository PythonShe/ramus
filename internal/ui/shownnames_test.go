package ui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// The names of a branch, an upstream, a worktree and a file are shown
// without the bidi overrides that could disguise them, in labels, menus
// and tooltips as in the text, as the history filter's menu shows them.
func TestNamesShownWithoutControls(t *testing.T) {
	const bad = "‮"
	cases := []struct {
		name string
		// show brings up what shows the names, a menu among it left open;
		// shown is a text it shows once there, and item an item of the
		// menu.
		show        func(h *harness, tb *tab)
		shown, item string
	}{
		{"reflog menu", func(h *harness, tb *tab) {
			h.click("Reflog")
			h.app.Settings.NoSidebar = true // where the branches are listed too
			tb.reflog.ref = "refs/heads/ot" + bad + "her"
			h.until("the button", h.shows("other"))
			h.click("other")
		}, "other", "evlin"},
		{"commit menu", func(h *harness, tb *tab) {
			h.click("History")
			h.until("the commit", h.shows("one"))
			h.rightClickRow(ui.RoleListItem, "one")
		}, "one", "Reset evlin to Here"},
		{"file menu", func(h *harness, tb *tab) {
			h.rightClickRow(ui.RoleListItem, "untracked fd.txt")
		}, "Commit to evlin", "Ignore fd.txt"},
		{"worktrees", func(h *harness, tb *tab) { tb.sel = "worktrees" }, "wtx", ""},
		{"worktree panel", func(h *harness, tb *tab) { h.click("Worktree Changes") }, "wtx", ""},
		{"branches", func(h *harness, tb *tab) { tb.sel = "refs" }, "in wtx", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			r := h.repoWith()
			r.Git("branch", "ot"+bad+"her")
			r.Git("switch", "-q", "-c", "ev"+bad+"lin")
			base, _ := filepath.EvalSymlinks(t.TempDir())
			t.Cleanup(h.closeAll) // before the folder above goes
			r.Git("worktree", "add", "-q", "-b", "side", filepath.Join(base, "wt"+bad+"x"))
			if err := os.WriteFile(filepath.Join(r.Dir, "f"+bad+"d.txt"), []byte("x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			h.trust.Set(r.GitDir(), true)
			h.app.Open(r.Dir)
			h.until("open", h.shows("Commit to evlin"))
			tb := h.app.current()
			c.show(h, tb)
			h.until("the names", h.shows(c.shown))
			var shown []string
			shown = append(shown, h.tt.Texts()...)
			shown = append(shown, h.tt.Menu()...)
			for _, a := range h.tt.AccessAll() {
				shown = append(shown, a.Name, a.Value, a.Description)
			}
			for _, s := range shown {
				if strings.Contains(s, bad) {
					t.Errorf("shown as is: %q", s)
				}
			}
			if c.item != "" && !slices.Contains(h.tt.Menu(), c.item) {
				t.Errorf("the menu has %q, not %q", h.tt.Menu(), c.item)
			}
		})
	}
}
