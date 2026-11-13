package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/trust"
	"ramus/internal/library"
	"ramus/internal/settings"
)

// harness runs the app's view headless; Update queues functions that pump
// runs on the test goroutine, as the main thread would.
type harness struct {
	t       *testing.T
	app     *App
	tt      *ui.Tester
	updates chan func()
	trust   *trust.Store
	lib     *library.Library
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	ts, err := trust.Load(filepath.Join(dir, "trust.json"))
	if err != nil {
		t.Fatal(err)
	}
	lib, err := library.Load(filepath.Join(dir, "library.json"))
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, updates: make(chan func(), 1024), trust: ts, lib: lib}
	h.app = NewApp(Deps{Runner: testrun.Runner(t), Trust: ts, Library: lib, Settings: settings.Defaults,
		Update: func(fn func()) { h.updates <- fn }})
	h.tt = ui.NewTester(h.app.View, 1200, 800)
	return h
}

// until pumps updates and frames until ok holds.
func (h *harness) until(what string, ok func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		h.tt.Frame()
		if ok() {
			return
		}
		select {
		case fn := <-h.updates:
			fn()
		case <-time.After(20 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %s; texts %q", what, h.tt.Texts())
		}
	}
}

func (h *harness) shows(s string) func() bool { return func() bool { return h.tt.HasText(s) } }

func (h *harness) click(s string) {
	h.t.Helper()
	if err := h.tt.Click(s); err != nil {
		h.t.Fatalf("click %q: %v; texts %q", s, err, h.tt.Texts())
	}
	h.tt.Frame()
}

// repoWith makes a repository the harness closes, and waits for, before
// the repository's folder is removed.
func (h *harness) repoWith(files ...string) *gittest.Repo {
	t := h.t
	r := gittest.Init(t)
	t.Cleanup(h.closeAll)
	r.Write("README", "hello")
	r.Commit("one")
	for _, f := range files {
		r.Write(f, "x")
	}
	return r
}

func (h *harness) closeAll() {
	var lives []<-chan struct{}
	for _, t := range h.app.tabs {
		if t.s.Repo != nil {
			lives = append(lives, t.s.Repo.Life().Done())
		}
	}
	h.app.CloseAll()
	for _, d := range lives {
		select {
		case <-d:
		case <-time.After(10 * time.Second):
			h.t.Error("a repository did not close")
		}
	}
}

func TestWelcome(t *testing.T) {
	h := newHarness(t)
	h.tt.Frame()
	for _, s := range []string{"Ramus", "Open Repository…", "Clone…"} {
		if !h.tt.HasText(s) {
			t.Fatalf("missing %q in %q", s, h.tt.Texts())
		}
	}
	r := h.repoWith()
	h.lib.Opened(r.Dir, time.Unix(1_700_000_000, 0))
	h.until("the recent repository", h.shows(filepath.Base(r.Dir)))
	h.click(filepath.Base(r.Dir))
	h.until("the opened repository", h.shows("No changes"))
}

func TestOpenFollowsChanges(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("clean status", h.shows("No changes"))
	if h.tt.HasText("Read-only") {
		t.Fatal("trusted repository shown read-only")
	}
	r.Write("new.txt", "x")
	h.until("the new file", h.shows("Changes (1)"))
	if !h.tt.HasText("new.txt") || !h.tt.HasText("main") {
		t.Fatalf("texts %q", h.tt.Texts())
	}
	h.click("new.txt")
	if h.app.current().chosen != 0 {
		t.Fatal("click did not choose the file")
	}
}

func TestWorktreesInSidebar(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	h.trust.Set(r.GitDir(), true)
	other := filepath.Join(t.TempDir(), "second")
	t.Cleanup(h.closeAll) // before the folder above goes
	r.Git("worktree", "add", "-q", other, "-b", "topic")
	h.app.Open(r.Dir)
	label := "second  topic"
	h.until("the worktree", h.shows(label))
	h.click(label)
	h.until("its tab", func() bool { return len(h.app.tabs) == 2 })
	if h.app.current().s.Path != library.Clean(other) {
		t.Fatalf("opened %s", h.app.current().s.Path)
	}
}

func TestTrustBanner(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	h.app.Open(r.Dir)
	h.until("the banner", h.shows("Read-only"))
	h.click("Trust")
	h.until("the banner to go", func() bool { return !h.tt.HasText("Read-only") && h.tt.HasText("No changes") })
	if !h.trust.Trusted(r.GitDir()) {
		t.Fatal("trust not recorded")
	}
}

func TestTabs(t *testing.T) {
	h := newHarness(t)
	a, b := h.repoWith(), h.repoWith()
	h.app.Open(a.Dir)
	h.app.Open(b.Dir)
	h.app.Open(a.Dir)
	if len(h.app.tabs) != 2 || h.app.active != 0 {
		t.Fatalf("tabs %d active %d", len(h.app.tabs), h.app.active)
	}
	h.until("both tabs", h.shows(filepath.Base(b.Dir)))
	h.app.Move(0, 1)
	if h.app.tabs[1].s.Path != library.Clean(a.Dir) || h.app.active != 1 {
		t.Fatal("move")
	}
	h.tt.Frame()
	h.click("Close " + filepath.Base(a.Dir))
	if len(h.app.tabs) != 1 || h.app.current().s.Path != library.Clean(b.Dir) {
		t.Fatal("close")
	}
	h.tt.Key(ui.Cmd, ui.KeyW)
	h.tt.Frame()
	if len(h.app.tabs) != 0 || !h.tt.HasText("Open Repository…") {
		t.Fatalf("Cmd+W: %q", h.tt.Texts())
	}
}

func TestNewRepository(t *testing.T) {
	h := newHarness(t)
	dir := filepath.Join(t.TempDir(), "fresh")
	t.Cleanup(h.closeAll)
	h.app.PickFolder = func(string) (string, error) { return dir, nil }
	h.tt.Frame()
	h.click("New…")
	h.until("the new repository", func() bool { return len(h.app.tabs) == 1 && h.tt.HasText("No changes") })
}

func TestDropOpens(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	h.tt.Frame()
	if !h.tt.DropFiles(600, 400, r.Dir) {
		t.Fatal("drop not taken")
	}
	h.tt.Frame()
	if len(h.app.tabs) != 1 {
		t.Fatal("dropped folder not opened")
	}
}

func TestPaletteAndClone(t *testing.T) {
	h := newHarness(t)
	src := h.repoWith()
	h.tt.Frame()
	h.tt.Key(ui.Cmd|ui.Shift, ui.KeyP)
	h.tt.Frame()
	h.tt.Type("clone")
	h.tt.Frame()
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	h.until("the clone dialog", h.shows("Clone a repository"))
	h.app.clone.url = "file://" + src.Dir
	h.app.clone.dir = filepath.Join(t.TempDir(), "copy")
	h.tt.Frame()
	h.click("Clone")
	h.until("the clone to open", func() bool { return len(h.app.tabs) == 1 && h.tt.HasText("No changes") })
	if _, err := os.Stat(filepath.Join(h.app.clone.dir, "README")); err == nil {
		t.Fatal("dialog state kept")
	}
}

func TestSettings(t *testing.T) {
	h := newHarness(t)
	h.app.SettingsPath = filepath.Join(t.TempDir(), "settings.json")
	var theme string
	h.app.ApplyTheme = func(s string) { theme = s }
	h.tt.Frame()
	h.tt.Key(ui.Cmd, ui.KeyComma)
	h.until("the settings", h.shows("Appearance"))
	h.app.settingsUI.theme = 2
	h.app.settingsUI.editor = "code --wait"
	h.tt.Frame()
	h.click("Save")
	if !h.tt.HasText("full path") {
		t.Fatalf("relative editor accepted: %q", h.tt.Texts())
	}
	exe, _ := os.Executable()
	h.app.settingsUI.editor = `"` + exe + `" --wait`
	h.tt.Frame()
	h.click("Save")
	if theme != "dark" || h.app.Settings.Theme != "dark" || len(h.app.Settings.Editor) != 2 || h.app.Settings.Editor[0] != exe {
		t.Fatalf("applied %q %+v", theme, h.app.Settings)
	}
	h.until("the file", func() bool {
		s, err := settings.Load(h.app.SettingsPath)
		return err == nil && s.Theme == "dark"
	})
}

func TestSplitArgs(t *testing.T) {
	for in, want := range map[string][]string{
		`/usr/bin/code --wait`:          {"/usr/bin/code", "--wait"},
		`"/Applications/My Editor.app"`: {"/Applications/My Editor.app"},
		`a 'b c' "d'e"`:                 {"a", "b c", "d'e"},
		"  ":                            nil,
	} {
		got := splitArgs(in)
		if strings.Join(got, "|") != strings.Join(want, "|") || len(got) != len(want) {
			t.Errorf("splitArgs(%q) = %q", in, got)
		}
		if back := splitArgs(joinArgs(got)); strings.Join(back, "|") != strings.Join(got, "|") {
			t.Errorf("join round trip of %q: %q", got, back)
		}
	}
}

func TestManagerAndGroups(t *testing.T) {
	h := newHarness(t)
	a, b := h.repoWith(), h.repoWith()
	h.lib.Opened(a.Dir, time.Unix(1_700_000_000, 0))
	h.lib.Opened(b.Dir, time.Unix(1_700_000_100, 0))
	h.tt.Frame()
	h.tt.Key(ui.Cmd|ui.Shift, ui.KeyO)
	h.until("the manager", h.shows("Repositories"))
	h.click("Mark " + filepath.Base(a.Dir) + " as favourite")
	h.until("the favourite", func() bool { return len(h.lib.Favourites()) == 1 })
	h.app.manager.query = filepath.Base(b.Dir)
	h.until("the search", func() bool { return len(h.app.manager.repos) == 1 })
	h.click(filepath.Base(b.Dir))
	h.until("the tab", func() bool { return len(h.app.tabs) == 1 && !h.app.manager.open })

	h.app.Open(a.Dir)
	h.app.openManager()
	h.app.manager.groupName = "Work"
	h.tt.Frame()
	h.click("Save Open Tabs as Group")
	h.until("the group", func() bool { return len(h.lib.Groups()) == 1 && h.tt.HasText("Work (2)") })
	h.app.CloseAll()
	h.tt.Frame()
	h.app.openManager()
	h.tt.Frame()
	h.click("Open Work")
	if len(h.app.tabs) != 2 {
		t.Fatalf("group opened %d tabs", len(h.app.tabs))
	}
}

func TestActionsAndLog(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	h.trust.Set(r.GitDir(), true)
	h.app.Settings.Actions = []settings.Action{
		{Name: "Say hello", Command: []string{"/bin/sh", "-c", "echo hello $1 on $2", "sh", "${prompt:Who}", "${branch}"}},
		{Name: "Count bytes", Command: []string{"/usr/bin/wc", "-c", "${file}"}, On: "file"},
	}
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	h.app.palette.open = true
	h.tt.Frame()
	h.tt.Type("say hello")
	h.tt.Frame()
	h.tt.Key(0, ui.KeyEnter)
	h.until("the question", h.shows("Who"))
	h.tt.Type("world")
	h.tt.Key(0, ui.KeyEnter)
	h.until("the output", h.shows("hello world on main"))

	r.Write("f.txt", "12345")
	h.until("the change", h.shows("Changes (1)"))
	h.click("Changes (1)")
	h.until("the file", h.shows("f.txt"))
	if err := h.tt.RightClick("f.txt"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("Count bytes"); err != nil {
		t.Fatalf("menu %q: %v", h.tt.Menu(), err)
	}
	h.until("the second output", h.shows("5 "))
}
