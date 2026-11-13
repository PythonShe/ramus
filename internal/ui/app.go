// Package ui holds the app's state and the views built from it.
package ui

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/redact"
	"ramus/internal/git/run"
	"ramus/internal/git/trust"
	"ramus/internal/git/write"
	"ramus/internal/library"
	"ramus/internal/session"
	"ramus/internal/settings"
	"ramus/internal/tools"
)

// Deps are what the app is built on; main fills them in, tests fake them.
type Deps struct {
	Runner   *run.Runner
	Trust    *trust.Store
	Library  *library.Library
	Settings settings.Settings
	// SettingsPath is the settings file, for saving and for "Open
	// Settings File".
	SettingsPath string
	// CacheDir keeps what is worth keeping across launches but can be
	// read again; "" keeps nothing.
	CacheDir string
	// Update runs a function on the main thread and rebuilds the view
	// (Window.Update). It returns at once.
	Update func(func())
	// PickFolder asks the user for a folder and blocks until they answer;
	// "" when they cancel. It is only called off the main thread.
	PickFolder func(title string) (string, error)
	// PickFile asks for a file to open, and SaveFile for where to save
	// one named name; both block, return "" when the user cancels, and are
	// only called off the main thread.
	PickFile func(title string) (string, error)
	SaveFile func(title, name string) (string, error)
	// ApplyTheme makes "system", "light" or "dark" the app's appearance.
	ApplyTheme func(theme string)
	// Now is the clock, for tests.
	Now func() time.Time
}

// App is the state the main window shows. View reads it on the main
// thread; other goroutines change it through Deps.Update.
type App struct {
	Deps
	tabs   []*tab
	active int
	nextID int
	// notice is a message for the user that does not belong to a tab.
	notice string
	// pending are repositories asked for before git was found.
	pending []string
	// gitWarning says the installed git is missing security fixes.
	gitWarning string
	// clip is text to put on the clipboard on the next frame: work read
	// off the main thread cannot reach the clipboard itself.
	clip string

	welcome    welcomeState
	clone      cloneState
	palette    paletteState
	settingsUI settingsState
	manager    managerState
	prompt     promptState
	links      linksDialog
	branchUI   branchDialog
	stashUI    stashDialog
	cleanUI    cleanState
	libStats   libStatsState
	diff       struct {
		diffOptions
		loaded bool
	}
	// confirm asks before a write that drops something.
	confirm confirmState
}

// tab is one open repository.
type tab struct {
	id     int
	s      *session.Session
	sel    string // sidebar item chosen
	open   map[string]bool
	cv     changesState
	commit commitState
	// The command log: entries newest first, and the output of one.
	logList   ui.ListState
	logChosen int
	outList   ui.ListState
	snaps     snapState
	hist      historyState
	reflog    reflogState
	refs      refsState
	stash     stashState
	bisect    bisectState
	stats     statsState
	links     tabLinks
	// expired is set once old snapshots were cleared on open.
	expired bool
	sideW   float32
	listW   float32
}

// NewApp returns the app with nothing open.
func NewApp(d Deps) *App {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Update == nil {
		d.Update = func(fn func()) { fn() }
	}
	a := &App{Deps: d, active: -1}
	a.findTools()
	return a
}

// findTools looks for the installed editors and terminals, and those the
// settings name, off the main thread since it touches the disk.
func (a *App) findTools() {
	editor, terminal, update := a.Settings.Editor, a.Settings.Terminal, a.Update
	go func() {
		var found []tools.Tool
		if tl, err := tools.Custom("your editor", tools.Editor, editor); err == nil {
			found = append(found, tl)
		}
		if tl, err := tools.Custom("your terminal", tools.Terminal, terminal); err == nil {
			found = append(found, tl)
		}
		found = append(found, tools.Detect()...)
		update(func() { a.palette.tools, a.palette.found = found, true })
	}()
}

// SetRunner gives the app the git it runs, once it is found off the main
// thread, and opens what was asked for meanwhile; err says why there is
// none.
func (a *App) SetRunner(r *run.Runner, err error) {
	if err != nil {
		a.notice = "Git was not found: " + errText(err)
		return
	}
	a.Runner = r
	if v := r.Git().Version; v.SecurityUpdate() {
		a.gitWarning = "Git " + v.String() + " lacks security fixes: update git"
	}
	for _, p := range a.pending {
		a.Open(p)
	}
	a.pending = nil
}

// Open opens the repository at path in a tab, or shows the tab that has it.
func (a *App) Open(path string) {
	if a.Runner == nil {
		a.pending = append(a.pending, path)
		return
	}
	path = library.Clean(path)
	if i := slices.IndexFunc(a.tabs, func(t *tab) bool { return t.s.Path == path }); i >= 0 {
		a.active = i
		return
	}
	a.nextID++
	t := &tab{id: a.nextID, s: session.Open(a.Runner, a.Trust, path, a.Update), sideW: 220, listW: 420, snaps: newSnapState(), cv: newChangesState(), hist: newHistoryState(), reflog: newReflogState(), refs: newRefsState(), stash: newStashState(),
		open: map[string]bool{"branches": true, "remotes": true, "worktrees": true, "submodules": true}, sel: "changes"}
	a.tabs = append(a.tabs, t)
	a.active = len(a.tabs) - 1
	if a.Library != nil {
		lib, now := a.Library, a.Now()
		go lib.Opened(path, now) // writes a file: never on the main thread
	}
}

// Close closes the tab at i.
func (a *App) Close(i int) {
	if i < 0 || i >= len(a.tabs) {
		return
	}
	a.tabs[i].s.Close()
	a.tabs = slices.Delete(a.tabs, i, i+1)
	if a.active >= len(a.tabs) || a.active > i {
		a.active--
	}
	if a.active < 0 && len(a.tabs) > 0 {
		a.active = 0
	}
}

// Move puts the tab at from before the tab at to, as a drag leaves it.
func (a *App) Move(from, to int) {
	if from == to || from < 0 || to < 0 || from >= len(a.tabs) || to >= len(a.tabs) {
		return
	}
	cur := a.tabs[a.active]
	t := a.tabs[from]
	a.tabs = slices.Delete(a.tabs, from, from+1)
	a.tabs = slices.Insert(a.tabs, to, t)
	a.active = slices.Index(a.tabs, cur)
}

// CloseAll ends every session, when the window closes.
func (a *App) CloseAll() {
	for _, t := range a.tabs {
		t.s.Close()
	}
	a.tabs, a.active = nil, -1
}

func (a *App) current() *tab {
	if a.active < 0 || a.active >= len(a.tabs) {
		return nil
	}
	return a.tabs[a.active]
}

// pickAndOpen asks for a folder off the main thread and opens it.
func (a *App) pickAndOpen() {
	if a.PickFolder == nil {
		return
	}
	pick, update := a.PickFolder, a.Update
	go func() {
		p, err := pick("Open Repository")
		update(func() {
			switch {
			case err != nil:
				a.notice = errText(err)
			case p != "":
				a.Open(p)
			}
		})
	}()
}

// initAndOpen asks for a folder, makes it a repository, and opens it.
func (a *App) initAndOpen() {
	if a.PickFolder == nil || a.Runner == nil {
		return
	}
	pick, update, r, ts := a.PickFolder, a.Update, a.Runner, a.Trust
	go func() {
		p, err := pick("New Repository")
		if err == nil && p != "" {
			// A repository made here is the user's own; one that was already
			// in the folder came from elsewhere and still asks to be trusted.
			_, statErr := os.Lstat(filepath.Join(p, ".git"))
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			err = write.Init(ctx, r, p, false)
			cancel()
			if err == nil && os.IsNotExist(statErr) && ts != nil {
				ts.Set(filepath.Join(p, ".git"), true)
			}
		}
		update(func() {
			switch {
			case err != nil:
				a.notice = errText(err)
			case p != "":
				a.Open(p)
			}
		})
	}()
}

// View builds the main window.
func (a *App) View(c *ui.Context) {
	a.shortcuts(c)
	if a.clip != "" {
		c.WriteClipboard(a.clip)
		a.clip = ""
	}
	root := ui.Column(c).Fill().Background(c.Theme().Background)
	if files := root.DroppedFiles(); files != nil {
		for _, f := range files {
			a.Open(f)
		}
	}
	if root.FileDragOver() {
		root.Border(2, c.Theme().Accent)
	}
	root.Children(func() {
		if len(a.tabs) > 0 {
			a.tabBar(c)
		}
		if t := a.current(); t != nil {
			a.repoView(c, t)
		} else {
			a.welcomeView(c)
		}
	})
	a.cloneDialog(c)
	a.settingsView(c)
	a.managerView(c)
	a.libStatsView(c)
	a.promptView(c)
	a.linksView(c)
	a.branchView(c)
	a.stashDialogView(c)
	a.cleanView(c)
	a.confirmView(c, &a.confirm)
	a.paletteView(c)
}

func (a *App) shortcuts(c *ui.Context) {
	if c.Shortcut(ui.Cmd, ui.KeyO) {
		a.pickAndOpen()
	}
	if c.Shortcut(ui.Cmd, ui.KeyW) && a.current() != nil {
		a.Close(a.active)
	}
	if c.Shortcut(ui.Cmd|ui.Shift, ui.KeyP) || c.Shortcut(ui.Cmd, ui.KeyK) {
		a.palette.open = true
	}
	if c.Shortcut(ui.Cmd, ui.KeyComma) {
		a.openSettings()
	}
	if c.Shortcut(ui.Cmd|ui.Shift, ui.KeyO) {
		a.openManager()
	}
	if t := a.current(); t != nil && !c.FocusTakesText() {
		// In a text field these keys undo the typing.
		if c.Shortcut(ui.Cmd, ui.KeyZ) {
			a.undo(t, false)
		}
		if c.Shortcut(ui.Cmd|ui.Shift, ui.KeyZ) {
			a.undo(t, true)
		}
	}
	for i, k := range []ui.Key{ui.Key1, ui.Key2, ui.Key3, ui.Key4, ui.Key5, ui.Key6, ui.Key7, ui.Key8, ui.Key9} {
		if c.Shortcut(ui.Cmd, k) && i < len(a.tabs) {
			a.active = i
		}
	}
}

// title is what a tab and the window call a repository.
func (a *App) title(t *tab) string {
	return filepath.Base(t.s.Path)
}

// Notify shows a message that belongs to no repository.
func (a *App) Notify(msg string) { a.notice = redact.Shown(msg) }

// errText is an error as the app shows it: git's message without escape
// sequences or credentials.
func errText(err error) string { return redact.Shown(err.Error()) }

// PickAndOpen asks for a repository's folder and opens it.
func (a *App) PickAndOpen() { a.pickAndOpen() }

// ShowClone opens the clone dialog.
func (a *App) ShowClone() { a.clone.open = true }

// ShowSettings opens the settings.
func (a *App) ShowSettings() { a.openSettings() }

// ShowManager opens the list of every known repository.
func (a *App) ShowManager() { a.openManager() }

// ShowPalette opens the command palette.
func (a *App) ShowPalette() { a.palette.open = true }
