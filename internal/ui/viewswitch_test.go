package ui

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/gittest"
)

// The bar's switch goes between Changes and History with the sidebar
// hidden, counts the changes without growing, and has neither down while
// another page shows; the sidebar lists neither.
func TestViewSwitch(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	h.app.setSidebar(false)
	h.until("no sidebar", func() bool { return !hasExact(h, "Reflog") })

	pressed := func(name string) bool {
		i := slices.IndexFunc(h.tt.AccessAll(), func(a ui.AccessInfo) bool { return a.Role == ui.RoleToggleButton && a.Name == name })
		if i < 0 {
			t.Fatalf("no toggle %q", name)
		}
		return h.tt.AccessAll()[i].Checked
	}
	tb := h.app.current()
	if !pressed("Changes") || pressed("History") {
		t.Fatal("Changes not the one down")
	}
	h.click("History")
	h.until("the history", func() bool { return tb.sel == "history" && pressed("History") && !pressed("Changes") })
	// A click on the half down leaves it down.
	h.click("History")
	if tb.sel != "history" || !pressed("History") {
		t.Fatalf("History let go: on %q", tb.sel)
	}

	before, _ := h.tt.Find("History")
	r.Write("one", "x")
	h.until("the count", func() bool { return hasExact(h, "1") })
	if after, _ := h.tt.Find("History"); after != before {
		t.Fatalf("the count moved History from %v to %v", before, after)
	}
	h.click("Changes (1)")
	h.until("the changes", func() bool { return tb.sel == sideChanges && pressed("Changes (1)") })

	h.app.setSidebar(true)
	h.until("the sidebar", func() bool { return hasExact(h, "Reflog") })
	h.click("Reflog")
	h.until("the reflog", func() bool { return tb.sel == "reflog" })
	if pressed("Changes (1)") || pressed("History") {
		t.Fatal("a half down on the reflog")
	}
	for _, a := range h.tt.AccessAll() {
		if a.Role != ui.RoleToggleButton && (a.Name == "History" || a.Name == "Changes (1)") {
			t.Fatalf("the sidebar lists %q", a.Name)
		}
	}
}

// At the window's least width, long names of the repository and of its
// branch are cut so that the switch and the end of the bar keep in it.
func TestViewSwitchNarrow(t *testing.T) {
	const narrowest = 960 // minWindowW in windows.go
	h := newHarness(t)
	dir := filepath.Join(t.TempDir(), "a-repository-with-a-name-long-enough-to-want-all-of-its-room")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if d, err := filepath.EvalSymlinks(dir); err == nil {
		dir = d
	}
	r := gittest.Open(t, dir)
	r.Git("init", "-q", "-b", "a-branch-with-a-name-long-enough-to-want-all-of-its-room-and-more")
	r.Write("README", "hello")
	r.Commit("one")
	t.Cleanup(h.closeAll)
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	h.tt.SetSize(narrowest, 600)
	h.settle()
	for _, name := range []string{"Changes", "History", "Settings and About"} {
		box, ok := h.tt.Find(name)
		if !ok {
			t.Fatalf("no %q", name)
		}
		if box.W <= 0 || box.X < 0 || box.X+box.W > narrowest {
			t.Fatalf("%q at %v, out of a window %v wide", name, box, narrowest)
		}
	}
	sw, _ := h.tt.Find("History")
	if sw.W < viewSwitchW-1 {
		t.Fatalf("History squeezed to %v", sw.W)
	}
}
