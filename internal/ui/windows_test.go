package ui

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/layout"
	"ramus/internal/library"
	"ramus/internal/settings"
	"ramus/locales"
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
	h.until("both tabs", func() bool { return len(h.app.tabs) == 2 && h.tt.HasText("No local changes") })
	if !h.app.Has(one.Dir) || h.app.Has(other) {
		t.Fatal("Has")
	}

	// From the keyboard: an empty window.
	h.tt.Key(ui.Cmd|ui.Shift, ui.KeyN)
	h.tt.Frame()
	if len(opened) != 1 || opened[0] != nil {
		t.Fatalf("opened %q", opened)
	}

	// The second repository, from its menu in the picker: it leaves this
	// window.
	h.repoMenu(filepath.Base(two.Dir), "Move to New Window")
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
	// The diff's layout too.
	h.app.keepSettingsOr(locales.DiffSettingsNotSaved)
	if len(told) != 2 {
		t.Fatalf("told %+v", told)
	}
	s := h.app.Settings
	s.NoSidebar = false
	h.app.UseSettings(s)
	h.until("the sidebar back", h.shows("Repository sections"))
}

// Opening, finding and restoring a repository never resolve its path on
// the main thread, where a volume that stopped answering would freeze
// the window: a tab opens on the path as given, which is resolved off
// it, and one that turns out to be a repository open already under
// another name closes for that one.
func TestOpenResolvesOffTheMainThread(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	link := filepath.Join(tmp, "link")
	if err := os.Symlink(r.Dir, link); err != nil {
		t.Skip(err)
	}
	release := make(chan struct{})
	h.app.resolve = func(p string) string {
		<-release
		return library.Clean(p)
	}
	done := make(chan struct{})
	go func() {
		h.app.Open(r.Dir)
		h.app.Open(link)
		h.app.Has(link)
		h.app.Restore(layout.Window{Tabs: []layout.Tab{{Path: link}}})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("opening waited for the path to be resolved")
	}
	if len(h.app.tabs) != 2 || h.app.current().s.Path != link {
		t.Fatalf("before the paths are resolved: %d tabs", len(h.app.tabs))
	}
	close(release)
	h.until("the tab of the link to close", func() bool { return len(h.app.tabs) == 1 })
	if h.app.current().s.Path != r.Dir {
		t.Fatalf("shown %s", h.app.current().s.Path)
	}
}

// A repository another window has open is shown there, that window
// brought forward, rather than opened a second time here: two sessions
// would race for its index.lock and write over each other's draft.
func TestOpenShowsAnotherWindowsTab(t *testing.T) {
	h := newHarness(t)
	r, r2 := h.repoWith(), h.repoWith()
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	link := filepath.Join(tmp, "link")
	if err := os.Symlink(r.Dir, link); err != nil {
		t.Skip(err)
	}
	h.app.Open(r.Dir)
	h.app.Open(r2.Dir)
	other := NewApp(Deps{Runner: h.runner, Trust: h.trust, Library: h.lib, Settings: settings.Defaults,
		Update: func(fn func()) { h.updates <- fn }})
	shown := 0
	other.ShowOpen = func(p string) bool {
		if !h.app.Has(p) {
			return false
		}
		h.app.Open(p)
		shown++
		return true
	}
	other.Open(r.Dir)
	if len(other.tabs) != 0 || shown != 1 || h.app.current().s.Path != r.Dir {
		t.Fatalf("%d tabs here, shown %d", len(other.tabs), shown)
	}

	// Under another name, it is known once resolved.
	h.app.Open(r2.Dir)
	other.Open(link)
	h.note()
	for _, o := range other.tabs {
		h.seen[o.s] = true
	}
	h.until("the second tab to close", func() bool { return len(other.tabs) == 0 })
	if shown != 2 || h.app.current().s.Path != r.Dir {
		t.Fatalf("shown %d", shown)
	}
}
