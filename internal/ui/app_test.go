package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/run"
	"ramus/internal/git/trust"
	"ramus/internal/library"
	"ramus/internal/session"
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
	// seen are the sessions of every tab a frame showed, so that one a
	// test closed has stopped too before its folder is removed.
	seen map[*session.Session]bool
}

// note records the sessions of the tabs now open.
func (h *harness) note() {
	if h.seen == nil {
		h.seen = map[*session.Session]bool{}
	}
	for _, t := range h.app.tabs {
		h.seen[t.s] = true
	}
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessWith(t, testrun.Runner(t))
}

// newHarnessWith is newHarness with the app running git through r.
func newHarnessWith(t *testing.T, r *run.Runner) *harness {
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
	h.app = NewApp(Deps{Runner: r, Trust: ts, Library: lib, Settings: settings.Defaults,
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
		h.note()
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

// patchHas reports whether the viewer of pv is built and its new side
// holds added and its old side removed.
func patchHas(pv *patchView, added, removed string) func() bool {
	return func() bool {
		return pv.d != nil && strings.Contains(pv.d.New.String(), added) && strings.Contains(pv.d.Old.String(), removed)
	}
}

func (h *harness) click(s string) {
	h.t.Helper()
	h.note()
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
	h.note()
	var lives []<-chan struct{}
	for s := range h.seen {
		lives = append(lives, s.Done())
	}
	h.app.CloseAll()
	h.app.writes.Wait()
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
	h.until("the new file", h.shows("new.txt"))
	if !h.tt.HasText("Changes (1)") || !h.tt.HasText("main") {
		t.Fatalf("texts %q", h.tt.Texts())
	}
	h.click("new.txt")
	if r, ok := h.app.current().cv.chosenFile(); !ok || r.sec != secUnstaged {
		t.Fatal("click did not choose the file")
	}
	h.until("its patch", patchHas(&h.app.current().cv.diff.pv, "x", ""))
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
	if h.tt.HasText("Read-only") {
		t.Fatal("a repository the app made asks to be trusted")
	}
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
	if h.tt.HasText("Read-only") {
		t.Fatal("a repository the app cloned asks to be trusted")
	}
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
	// The whole path: the base name alone may be in the temporary
	// directory's random name too.
	h.app.manager.query = b.Dir
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
	sh := gittest.Shell(t)
	h.app.Settings.Actions = []settings.Action{
		{Name: "Say hello", Command: []string{sh, "-c", "echo hello $1 on $2", "sh", "${prompt:Who}", "${branch}"}},
		{Name: "Count bytes", Command: []string{sh, "-c", `wc -c "$1"`, "sh", "${file}"}, On: "file"},
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

func TestShowPath(t *testing.T) {
	for p, flagged := range map[string]bool{"src/a.go": false, "a\nb": true, ".GIT/config": true, "x/../y": true, "dir/git~1/x": true, "\x1b[31mred": true} {
		shown, why := showPath(p)
		if (why != "") != flagged || (flagged && shown == p) {
			t.Errorf("showPath(%q) = %q, %q", p, shown, why)
		}
	}
}

func TestSnapshotsView(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	r.Write("README", "work in progress")
	h.app.current().sel = "snapshots"
	h.until("the empty list", h.shows("No snapshots"))
	h.click("Snapshot Now")
	h.until("the snapshot", h.shows("Snapshot now"))
	h.until("its files", h.shows("README"))

	r.Write("README", "an agent broke this")
	h.click("Restore All")
	h.until("the restore", func() bool {
		b, _ := os.ReadFile(filepath.Join(r.Dir, "README"))
		return string(b) == "work in progress"
	})
	h.until("the state it replaced", h.shows("Before restoring"))

	h.tt.Key(ui.Cmd, ui.KeyZ)
	h.until("the undo in the log", func() bool {
		l := h.app.current().s.Log
		return len(l) > 0 && strings.HasPrefix(l[len(l)-1].Name, "Undo") && !l[len(l)-1].Running
	})
}

func TestChangesStageAndDiscard(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n")
	r.Write("dir/deep.txt", "d\n")
	r.Commit("a")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	r.Write("a.txt", "one\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\ntwelve\n")
	r.Write("new.txt", "x\n")
	h.until("the changes", func() bool { return h.tt.HasText("a.txt") && h.tt.HasText("new.txt") })
	tb := h.app.current()
	index := func() string { return r.Git("show", ":a.txt") }
	read := func(name string) string { b, _ := os.ReadFile(filepath.Join(r.Dir, name)); return string(b) }

	// Stage the untracked file from its menu.
	if err := h.tt.RightClick("new.txt"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("Stage File"); err != nil {
		t.Fatalf("%v: %q", err, h.tt.Menu())
	}
	h.until("new.txt staged", h.shows("Staged (1)"))

	// Stage the first change of a.txt only.
	h.click("a.txt")
	pv := &tb.cv.diff.pv
	h.until("its patch", patchHas(pv, "one", "1"))
	if !pv.whole || len(pv.runs) != 2 {
		t.Fatalf("whole %v, %d changes", pv.whole, len(pv.runs))
	}
	h.app.applyLines(tb, pv.changeLines(0), true, false)
	h.until("the first change staged", func() bool { return strings.HasPrefix(index(), "one\n") })
	if strings.Contains(index(), "twelve") {
		t.Fatal("the second change was staged too")
	}

	// Select the added line of the second change and discard it, then
	// undo.
	h.until("the patch again", func() bool { return pv.d != nil && pv.d.Old.String() != "" && len(pv.runs) == 1 })
	pv.d.SetSelectedLines(editor.DiffLineRange{Side: editor.DiffNew, Start: 11, End: 11}, true)
	h.until("the line picked", h.shows("Discard 1 Line…"))
	h.click("Discard 1 Line…")
	h.until("the question", h.shows("Discard 1 changed line?"))
	h.click("Discard")
	h.until("the line discarded", func() bool { return !strings.Contains(read("a.txt"), "twelve") })
	if !strings.HasPrefix(read("a.txt"), "one\n") {
		t.Fatalf("discard took more than the line: %q", read("a.txt"))
	}
	h.tt.Key(ui.Cmd, ui.KeyZ)
	h.until("the discard undone", func() bool { return strings.Contains(read("a.txt"), "twelve") })

	// Unstage everything from the section header; the tree shows folders.
	h.click("Unstage All")
	h.until("nothing staged", func() bool { return !h.tt.HasText("Staged (") })
	r.Write("dir/deep.txt", "changed\n")
	r.Write("loose/x", "x\n")
	h.click("Tree")
	h.until("the folder", h.shows("dir/"))
	if h.tt.HasText("deep.txt") {
		t.Fatal("a closed folder shows its files")
	}
	if !h.tt.HasText("loose/") || h.tt.HasText("Folder loose") {
		t.Fatalf("an untracked folder is not one row: %q", h.tt.Texts())
	}
	h.click("▸")
	h.until("the folder open", h.shows("deep.txt"))
	tb.cv.filter = "deep"
	h.until("the filter", func() bool { return !h.tt.HasText("a.txt") && h.tt.HasText("deep.txt") })
}

func TestChangesBulk(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	r.Write("one.txt", "1")
	r.Write("two.txt", "2")
	r.Write("three.txt", "3")
	h.until("the files", h.shows("three.txt"))
	h.click("one.txt")
	if err := h.tt.ClickWith(ui.Cmd, "two.txt"); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()
	if err := h.tt.RightClick("two.txt"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("Stage 2 Files"); err != nil {
		t.Fatalf("%v: %q", err, h.tt.Menu())
	}
	h.until("two staged", h.shows("Staged (2)"))
	if s := r.Git("status", "--porcelain"); !strings.Contains(s, "?? three.txt") {
		t.Fatalf("status\n%s", s)
	}
}

func TestCommitBox(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("lf.txt", "a\nb\n")
	r.Commit("lf")
	r.Write(".tpl", "Subject line\n\n# Explain why\n")
	r.Git("config", "commit.template", ".tpl")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("Commit to main"))
	tb := h.app.current()
	cs := &tb.commit
	h.until("the template", func() bool { return cs.subject == "Subject line" })
	if !strings.Contains(cs.body, "# Explain why") {
		t.Fatalf("body %q", cs.body)
	}

	// A commit with the template's comment stripped.
	r.Write("lf.txt", "a\nb\nc\n")
	r.Git("add", "lf.txt")
	h.until("staged", h.shows("Staged (1)"))
	cs.subject, cs.body = "Add c", "Because.\n\n# Explain why"
	h.click("Commit to main")
	h.until("the commit", func() bool { return strings.TrimSpace(r.Git("log", "-1", "--format=%B")) == "Add c\n\nBecause." })
	h.until("the editor cleared", func() bool { return cs.subject == "Subject line" && !cs.busy })

	// CRLF into an LF file asks first.
	r.Write("lf.txt", "a\r\nb\r\nc\r\n")
	r.Git("-c", "core.autocrlf=false", "add", "lf.txt")
	h.until("staged", h.shows("Staged (1)"))
	cs.subject, cs.body = "CRLF", ""
	h.click("Commit to main")
	h.until("the warning", h.shows("Line endings change in 1 file"))
	if !h.tt.HasText("core.autocrlf: not set") {
		t.Fatalf("settings not named: %q", h.tt.Texts())
	}
	h.click("Commit Anyway")
	h.until("the commit", func() bool { return strings.TrimSpace(r.Git("log", "-1", "--format=%s")) == "CRLF" })

	// Amend loads HEAD's message; recent messages are offered.
	h.until("ready again", func() bool { return !cs.busy && cs.setup != nil })
	cs.subject, cs.body = "", ""
	cs.amend = true
	h.tt.Frame()
	h.until("HEAD's message", func() bool { return cs.subject == "CRLF" })
	cs.subject = "CRLF line endings"
	h.click("Amend Last Commit")
	h.until("the amend", func() bool { return strings.TrimSpace(r.Git("log", "-1", "--format=%s")) == "CRLF line endings" })
	if n := strings.TrimSpace(r.Git("rev-list", "--count", "HEAD")); n != "4" {
		t.Fatalf("%s commits after an amend", n)
	}
	h.until("recent loaded", func() bool { return cs.setup != nil && len(cs.setup.Recent) >= 3 })

	// Commit and push sets the upstream on the one remote.
	remote := gittest.Init(t, "--bare")
	r.Git("remote", "add", "origin", remote.Dir)
	r.Write("lf.txt", "pushed\n")
	r.Git("add", "lf.txt")
	h.until("staged", h.shows("Staged (1)"))
	cs.subject, cs.body = "Pushed", ""
	h.click("Commit and Push")
	h.until("the push", func() bool {
		out, err := remote.Run(nil, "log", "-1", "--format=%s", "main")
		return err == nil && strings.TrimSpace(out) == "Pushed"
	})
}
