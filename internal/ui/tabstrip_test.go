package ui

import (
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/gittest"
)

// stripTabs is the tabs of the strip, in order, and whether it shows.
func (h *harness) stripTabs() ([]ui.AccessInfo, bool) {
	if _, ok := h.tt.AccessByRole(ui.RoleTabList, "Open Repositories"); !ok {
		return nil, false
	}
	var tabs []ui.AccessInfo
	for _, a := range h.tt.AccessAll() {
		if a.Role == ui.RoleTab {
			tabs = append(tabs, a)
		}
	}
	return tabs, true
}

// stripTab is the strip's tab named name.
func (h *harness) stripTab(name string) ui.AccessInfo {
	h.t.Helper()
	var tab ui.AccessInfo
	h.until("the tab "+name, func() bool {
		var ok bool
		tab, ok = h.tt.AccessByRole(ui.RoleTab, name)
		return ok
	})
	return tab
}

// openTwo opens two repositories, trusted, and waits for both tabs.
func (h *harness) openTwo() (first, second *tab) {
	h.t.Helper()
	r1, r2 := h.repoWith(), h.repoWith()
	h.trust.Set(r1.GitDir(), true)
	h.trust.Set(r2.GitDir(), true)
	h.app.Open(r1.Dir)
	h.app.Open(r2.Dir)
	h.until("the second repository", h.shows("No changes"))
	first, second = h.app.tabs[0], h.app.tabs[1]
	h.stripTab(h.app.title(first))
	return first, second
}

// The strip shows only while more than one repository is open, and its
// chosen tab follows the one shown, by a click or the next tab's keys.
func TestTabStripFollowsTheTabShown(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	if _, ok := h.stripTabs(); ok {
		t.Fatal("the strip shows with one repository open")
	}
	r2 := h.repoWith()
	h.app.Open(r2.Dir)
	h.until("the second repository", h.shows("No changes"))
	first, second := h.app.tabs[0], h.app.tabs[1]
	selected := func(tb *tab) func() bool {
		return func() bool {
			a, ok := h.tt.AccessByRole(ui.RoleTab, h.app.title(tb))
			return ok && a.Selected
		}
	}
	h.until("the second tab chosen", selected(second))
	if tabs, _ := h.stripTabs(); len(tabs) != 2 || tabs[0].Name != h.app.title(first) || tabs[1].Name != h.app.title(second) {
		t.Fatalf("tabs %+v", tabs)
	}
	a := h.stripTab(h.app.title(first))
	h.tt.ClickAt(a.Bounds.X+a.Bounds.W/2, a.Bounds.Y+a.Bounds.H/2)
	h.until("the first tab chosen", selected(first))
	if h.app.current() != first {
		t.Fatal("the click did not show the first repository")
	}
	h.tt.Key(ui.Ctrl, ui.KeyTab)
	h.until("the next tab chosen", selected(second))
	if h.app.current() != second {
		t.Fatal("the key did not show the next repository")
	}
	// Closing down to one repository takes the strip away.
	h.app.Close(0)
	h.until("the strip gone", func() bool { _, ok := h.stripTabs(); return !ok })
}

// Two repositories of one name are told apart by the folders above them.
func TestTabStripTellsNamesApart(t *testing.T) {
	h := newHarness(t)
	web, mobile := gittest.Init(t, "web/app"), gittest.Init(t, "mobile/app")
	t.Cleanup(h.closeAll)
	h.app.Open(filepath.Join(web.Dir, "web", "app"))
	h.app.Open(filepath.Join(mobile.Dir, "mobile", "app"))
	h.stripTab("web/app")
	h.stripTab("mobile/app")
}

// A tab says when its repository has changes not committed, and when a
// write runs there.
func TestTabStripShowsState(t *testing.T) {
	h := newHarness(t)
	dirty := h.repoWith("new.txt")
	clean := h.repoWith()
	h.trust.Set(clean.GitDir(), true)
	h.app.Open(dirty.Dir)
	h.app.Open(clean.Dir)
	h.until("the clean repository", h.shows("No changes"))
	h.stripTab(h.app.title(h.app.tabs[0]) + ", uncommitted changes")
	tb := h.app.tabs[1]
	h.stripTab(h.app.title(tb))
	h.hang(tb)
	h.stripTab(h.app.title(tb) + ", Slow push running")
	for _, e := range tb.s.Log {
		e.Cancel()
	}
	h.stripTab(h.app.title(tb))
}

// The close button closes its tab as Close Tab does: asking first while
// a write runs there.
func TestTabStripCloseButton(t *testing.T) {
	h := newHarness(t)
	_, second := h.openTwo()
	name := h.app.title(second)
	h.hang(second)
	h.stripTab(name + ", Slow push running")
	h.click("Close " + name)
	h.until("the question", h.shows("Stop and Close"))
	h.click("Cancel")
	if len(h.app.tabs) != 2 || busyWrite(second) == nil {
		t.Fatal("the tab closed, or its write stopped, before the user said so")
	}
	h.click("Close " + name)
	h.until("the question again", h.shows("Stop and Close"))
	h.click("Stop and Close")
	h.until("closed", func() bool { return len(h.app.tabs) == 1 })
	if h.app.tabs[0] == second {
		t.Fatal("closed the other tab")
	}
}

// The middle button closes the tab it lets go over.
func TestTabStripMiddleClickCloses(t *testing.T) {
	h := newHarness(t)
	first, _ := h.openTwo()
	a := h.stripTab(h.app.title(first))
	x, y := a.Bounds.X+a.Bounds.W/2, a.Bounds.Y+a.Bounds.H/2
	h.tt.PressButton(2, x, y)
	h.tt.Frame()
	if len(h.app.tabs) != 2 {
		t.Fatal("closed on the press, before the button let go")
	}
	h.tt.ReleaseButton(2, x, y)
	h.until("closed", func() bool { return len(h.app.tabs) == 1 })
	if h.app.tabs[0] == first {
		t.Fatal("closed the other tab")
	}
}

// Dragging a tab onto another puts it in that one's place, the tab shown
// staying shown.
func TestTabStripDragReorders(t *testing.T) {
	h := newHarness(t)
	first, second := h.openTwo()
	from := h.stripTab(h.app.title(first)).Bounds
	to := h.stripTab(h.app.title(second)).Bounds
	y := from.Y + from.H/2
	h.tt.Press(from.X+from.W/2, y)
	h.tt.Frame()
	for x := from.X + from.W/2; x < to.X+to.W/2; x += 20 {
		h.tt.Move(x, y)
		h.tt.Frame()
	}
	h.tt.Move(to.X+to.W/2, y)
	h.tt.Frame()
	h.tt.Release(to.X+to.W/2, y)
	h.until("the tabs swapped", func() bool { return len(h.app.tabs) == 2 && h.app.tabs[0] == second })
	if h.app.current() != second {
		t.Fatal("the drag changed the tab shown")
	}
	h.until("the strip in the new order", func() bool {
		tabs, _ := h.stripTabs()
		return len(tabs) == 2 && tabs[0].Name == h.app.title(second)
	})
}

// Tabs past what the window has room for scroll, the tab shown kept in
// view as it changes.
func TestTabStripScrollsToTheTabShown(t *testing.T) {
	h := newHarness(t)
	h.tt.SetSize(500, 800)
	for range 6 {
		r := h.repoWith()
		h.app.Open(r.Dir)
	}
	h.until("the last repository", h.shows("No changes"))
	inView := func(tb *tab) func() bool {
		return func() bool {
			a, ok := h.tt.AccessByRole(ui.RoleTab, h.app.title(tb))
			return ok && a.Selected && a.Bounds.X >= 0 && a.Bounds.X+a.Bounds.W <= 500
		}
	}
	h.until("the last tab in view", inView(h.app.tabs[5]))
	h.tt.Key(ui.Ctrl, ui.KeyTab)
	h.until("the first tab in view", inView(h.app.tabs[0]))
}
