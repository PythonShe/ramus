// Package ui holds the app's state and the views built from it.
package ui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/crashlog"
	"ramus/internal/drafts"
	"ramus/internal/forge"
	"ramus/internal/git/config"
	"ramus/internal/git/gitexe"
	"ramus/internal/git/redact"
	"ramus/internal/git/repo"
	"ramus/internal/git/run"
	"ramus/internal/git/trust"
	"ramus/internal/git/write"
	"ramus/internal/layout"
	"ramus/internal/library"
	"ramus/internal/session"
	"ramus/internal/settings"
	"ramus/internal/tools"
	"ramus/locales"
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
	// SettingsKeeper writes the settings file for every window, so that
	// their saves land in order; nil has the app keep its own.
	SettingsKeeper *settings.Keeper
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
	// Program is what starts the app, its executable or its AppImage
	// (cli.Program), for the git config that makes it git's difftool and
	// mergetool; "" offers none.
	Program string
	// ApplyTheme makes "system", "light" or "dark" the app's appearance.
	ApplyTheme func(theme string)
	// Now is the clock, for tests.
	Now func() time.Time
	// Crash keeps the crash reports; nil keeps none.
	Crash *crashlog.Log
	// KeysChanged is called on the main thread when the shortcuts
	// change, for the app menu to show them; Accelerator reads them.
	KeysChanged func()
	// RoleKeys are the shortcuts of the app menu's items with a role (as
	// Minimize in the Window menu), by accelerator, with the labels the
	// menu shows, where a command bound to one of them takes it from the
	// item: the window takes a key before those items do. Nil, or nil
	// returned, where none is. Called on the main thread.
	RoleKeys func() map[string]string
	// RepoCommandsChanged is called on the main thread when the app
	// menu's Repository items come to act on the window, or stop
	// (RepoCommandsOn).
	RepoCommandsChanged func(on bool)
	// NewWindow opens a window showing the repositories at paths, none
	// for an empty one; nil offers no other window.
	NewWindow func(paths []string)
	// ShowOpen shows the repository at path in another window that has
	// it open, bringing that window forward, and reports whether one
	// had; nil looks in no other window. Called on the main thread.
	ShowOpen func(path string) bool
	// ShowFileOpen shows the file or folder abs, blamed when blame is
	// set, in another window that has its repository open (the one at
	// dir, or one whose work tree holds abs), bringing that window
	// forward, and reports whether one had; nil looks in no other
	// window. Called on the main thread.
	ShowFileOpen func(dir, abs string, blame bool) bool
	// ShowAbout opens the About window, where the check for updates is;
	// nil offers none.
	ShowAbout func()
	// SettingsChanged is called on the main thread with the settings this
	// window changed, for the other windows to use them.
	SettingsChanged func(settings.Settings)
	// Drafts keeps each repository's unfinished commit message; nil
	// keeps none.
	Drafts *drafts.Store
	// LayoutChanged is called on the main thread when the tabs open, or
	// what is chosen in them, changed, for Layout to be kept.
	LayoutChanged func()
	// Languages are the languages the app is written in, as BCP 47 tags.
	// ChosenLocale returns the one chosen in Settings, "" while the app
	// follows the system's, and SetLocale chooses one, "" to follow the
	// system again; mygo keeps the choice across launches. Settings
	// offers no language while SetLocale is nil.
	Languages    []string
	ChosenLocale func() string
	SetLocale    func(tag string)
}

// App is the state the main window shows. View reads it on the main
// thread; other goroutines change it through Deps.Update.
type App struct {
	themes themes
	Deps
	tabs   []*tab
	active int
	nextID int
	strip  tabStrip
	// writes are the files being written off the main thread, for a quit
	// and a test to wait for.
	writes sync.WaitGroup
	// refPrefsWrites orders the writes of each repository's pinned and
	// hidden refs.
	refPrefsWrites lastWrites
	// keeper writes the settings when Deps has none.
	keeper *settings.Keeper
	// toolsGen counts the looks for tools started, the last one's result
	// the only one taken; toolsErr is why the settings' editor or
	// terminal cannot be used, as last said.
	toolsGen int
	toolsErr string
	// notice is the last message for the user, noticeBy whom it belongs
	// to: a tab, or the window.
	notice   string
	noticeBy noticeOwner
	// windowNotes are what Notify said, for every tab and the welcome,
	// each until dismissed.
	windowNotes []string
	// noteShown is the message whose whole text Details shows, "" none.
	noteShown string
	// pending are repositories asked for before git was found.
	pending []string
	// restoring are the tabs Restore opens, by path, to set up when each
	// is made; lastLayout the layout LayoutChanged was last told of.
	restoring  map[string]restoredTab
	lastLayout layout.Window
	// resolve resolves a tab's path off the main thread, library.Clean
	// when nil; a test makes it slow.
	resolve func(string) string
	// gitWarning says the installed git is missing security fixes.
	gitWarning string
	// clip is text to put on the clipboard on the next frame: work read
	// off the main thread cannot reach the clipboard itself.
	clip string
	// toasts wait for the next frame to show, for the same reason: what
	// ends a write runs without the window's context.
	toasts []ui.Toast
	// openURL is a page to open in the browser.
	openURL string
	// forges finds gh and glab, forgeFind in their place for a test;
	// forgeWarned are the notices about them said once.
	forges      *forgeTools
	forgeFind   func(forge.Kind) (*forge.CLI, error)
	forgeWarned map[string]bool
	ghStack     stackTool

	keymap *keymap
	// find are the fields Find focuses; repoOn is RepoCommandsOn, as
	// RepoCommandsChanged was last told.
	find   findFields
	repoOn bool
	// modals counts the dialogs this frame shows, modalUp whether the
	// last one showed any (DialogOpen).
	modals     int
	modalUp    bool
	welcome    welcomeState
	repoPick   repoPick
	clone      cloneState
	palette    paletteState
	settingsUI settingsState
	keysUI     keysState
	diag       diagState
	manager    managerState
	prompt     promptState
	links      linksDialog
	branchUI   branchDialog
	// worktreeUI, worktreeRemoveUI and worktreeLockUI make, remove and
	// lock worktrees.
	worktreeUI       worktreeDialog
	worktreeRemoveUI worktreeRemoval
	worktreeLockUI   worktreeLock
	// remoteUI checks out a remote branch a local one has the name of;
	// elsewhereUI explains a branch checked out in another worktree.
	remoteUI    remoteCheckout
	elsewhereUI elsewhere
	// asking are the questions from git and ssh, as for a password.
	asking  asking
	message messageDialog
	history historyEdit
	stashUI stashDialog
	cleanUI cleanState
	mergeUI mergeDialog
	// pendingFile is a file to show once its repository is open.
	pendingFile pendingFile
	compareUI   compareState
	toolMerge   mergeTool
	// tool is set when the app runs as git's difftool or mergetool.
	tool     bool
	libStats libStatsState
	diff     struct {
		diffOptions
		loaded bool
	}
	// confirm asks before a write that drops something.
	confirm confirmState
	// shownTab is the tab shown on the last frame, to put the one left in
	// the background.
	shownTab *tab
}

// tab is one open repository.
type tab struct {
	id int
	s  *session.Session
	// real is the session's path with symbolic links resolved, "" until
	// resolveTab has.
	real string
	// top is the top of the repository found at real (its git directory
	// when bare), "" until resolveTab has or when there is none.
	top string
	sel string // sidebar item chosen
	// shown is the view the session was last told is shown.
	shown  string
	open   map[string]bool
	cv     changesState
	commit commitState
	// seq is a continue, skip or abort of a stopped operation on its way.
	seq seqState
	// The command log: entries newest first, and the output of one.
	logList   ui.ListState
	logChosen int
	outList   ui.ListState
	gitLog    gitLogState
	snaps     snapState
	hist      historyState
	reflog    reflogState
	refs      refsState
	stash     stashState
	locks     lockState
	pr        prState
	merge     mergeState
	file      fileState
	bisect    bisectState
	stats     statsState
	cmp       commitCompare
	dash      dashState
	rev       reviewState
	pulls     pullsState
	forcePush forcePush
	links     tabLinks
	prefs     refPrefs
	sideRefs  sideRefs
	pick      branchPick
	sync      syncState
	wtp       wtPanel
	// expired is set once old snapshots were cleared on open.
	expired bool
	sideW   float32
	// sideOut is the width of the hidden sidebar's pane, 0 until its
	// divider is dragged out.
	sideOut float32
	// listW is the width of a page's list beside what it shows of its
	// row chosen, and listRoom the room the two had when last laid out.
	listW, listRoom float32
	nav             tabNav
	// failSeen is the failed write whose error the user dismissed.
	failSeen *session.LogEntry
	// note is the tab's notice, see claimNotice.
	note tabNote
	// pics are the people's pictures decoded, by lower-case email, as of
	// the session's picsGen'th change of them.
	pics    map[string]*ui.Bitmap
	picsGen uint64
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
	loadZones()
	return a
}

// findTools looks for the installed editors and terminals, and those the
// settings name, off the main thread since it touches the disk. The first
// of each kind is the one used: the one the settings name, else the one
// the user prefers (tools.Preferred), else the first found.
func (a *App) findTools() {
	editor, terminal, update := a.Settings.Editor, a.Settings.Terminal, a.Update
	// Looks started one after the other may end in any order: only the
	// last one's tools are those of the settings now.
	a.toolsGen++
	gen := a.toolsGen
	a.goWork(nil, func() {
		found := tools.Detect()
		var auto [2]string
		var mine []tools.Tool
		var unusable []string
		for _, k := range []struct {
			kind tools.Kind
			name string
			argv []string
		}{{tools.Editor, yourEditor, editor}, {tools.Terminal, yourTerminal, terminal}} {
			if i := tools.Preferred(found, k.kind); i >= 0 {
				auto[k.kind] = found[i].Name
				found = append(append([]tools.Tool{found[i]}, found[:i]...), found[i+1:]...)
			} else if i := slices.IndexFunc(found, func(t tools.Tool) bool { return t.Kind == k.kind }); i >= 0 {
				auto[k.kind] = found[i].Name
			}
			if len(k.argv) == 0 {
				continue
			}
			// One found, chosen in the settings, goes first under its own
			// name; another program is the user's own.
			if i := slices.IndexFunc(found, func(t tools.Tool) bool {
				return t.Kind == k.kind && len(k.argv) == 1 && t.Program() == k.argv[0]
			}); i >= 0 {
				mine = append(mine, found[i])
				found = append(found[:i:i], found[i+1:]...)
			} else if tl, err := tools.Custom(k.name, k.kind, k.argv); err == nil {
				mine = append(mine, tl)
			} else if k.kind == tools.Editor {
				// Another is used in its place: the user is told why.
				unusable = append(unusable, locales.AppEditorUnusable(k.argv[0], errText(err)))
			} else {
				unusable = append(unusable, locales.AppTerminalUnusable(k.argv[0], errText(err)))
			}
		}
		found = append(mine, found...)
		update(func() {
			if gen != a.toolsGen {
				return
			}
			a.palette.tools, a.palette.found, a.palette.auto = found, true, auto
			// Said once, not at each look for the same settings.
			if msg := strings.Join(unusable, "\n"); msg != a.toolsErr {
				a.toolsErr = msg
				if msg != "" {
					a.notice = msg
				}
			}
		})
	})
}

// yourEditor and yourTerminal name the editor and the terminal the
// settings name. A tool's name is also its id, in the shortcuts and in
// the editor chosen for a repository, so it stays the same in every
// language and toolName says it.
const (
	yourEditor   = "your editor"
	yourTerminal = "your terminal"
)

// toolName is what the views call the tool named name.
func toolName(name string) string {
	switch name {
	case yourEditor:
		return locales.AppYourEditor()
	case yourTerminal:
		return locales.AppYourTerminal()
	}
	return name
}

// SetRunner gives the app the git it runs, once it is found off the main
// thread, and opens what was asked for meanwhile; err says why there is
// none.
func (a *App) SetRunner(r *run.Runner, err error) {
	var old *gitexe.TooOldError
	switch {
	case errors.As(err, &old):
		a.Notify(locales.AppGitTooOld(old.Version.String(), gitexe.Minimum.String()))
		return
	case err != nil:
		a.Notify(locales.AppGitNotFound(errText(err)))
		return
	}
	a.Runner = r
	// Editors and terminals are found on the user's PATH, which the app's
	// own lacks when started from a desktop, and started with their
	// environment, as git is.
	tools.UseEnviron(r.Environ())
	a.findTools()
	if v := r.Git().Version; v.SecurityUpdate() {
		a.gitWarning = locales.AppGitOutdated(v.String())
	}
	for _, p := range a.pending {
		a.Open(p)
	}
	a.pending = nil
	a.restoring = nil // a tab open twice is set up once
	if a.compareUI.pending {
		a.startCompare()
	}
	if a.toolMerge.pending {
		a.startMergeTool()
	}
}

// Open opens the repository at path in a tab, or shows the tab that has it.
func (a *App) Open(path string) {
	if a.Runner == nil {
		a.pending = append(a.pending, path)
		return
	}
	path = cleanPath(path)
	if i := slices.IndexFunc(a.tabs, func(t *tab) bool { return t.is(path) }); i >= 0 {
		a.active = i
		return
	}
	// Two windows on one repository would race for its index.lock and
	// write over each other's draft.
	if a.ShowOpen != nil && a.ShowOpen(path) {
		return
	}
	if len(a.tabs) == 0 && a.noticeBy.tab == nil {
		// The welcome's notice, of an open or clone that failed: not of
		// the repository opened now, whose tab would show it.
		a.dropNotice()
	}
	a.nextID++
	t := &tab{id: a.nextID, s: session.Open(a.Runner, a.Trust, path, a.Update), sideW: 220, listW: 420, snaps: newSnapState(), cv: newChangesState(), hist: newHistoryState(), reflog: newReflogState(), locks: newLockState(), pr: newPRState(), refs: newRefsState(), stash: newStashState(), file: newFileState(),
		open: map[string]bool{"branches": true, "remotes": true, "worktrees": true, "submodules": true}, sel: "changes"}
	t.s.ShowDependencyFolders(a.Settings.ShowDependencyFolders)
	a.tabs = append(a.tabs, t)
	r, restored := a.restoring[path]
	if restored {
		delete(a.restoring, path)
		r.apply(t)
	}
	if !restored || r.active {
		a.active = len(a.tabs) - 1
	}
	a.resolveTab(t)
	// Reopened at launch is not opened by the user: the library's order
	// stays.
	if a.Library != nil && !restored {
		lib, now, runner := a.Library, a.Now(), a.Runner
		// It reads and writes files: never on the main thread.
		a.goWrite(func() func() {
			// What is no repository (a plain folder, a file, a typo) stays
			// out, or it would be listed for good. A worktree added to a
			// repository is no repository of the library either: its
			// repository lists it, and it comes and goes.
			if !isRepository(path) || addedWorktree(runner, path) {
				return nil
			}
			lib.Opened(path, now)
			return nil
		})
	}
}

// isRepository reports whether path is a folder in a repository.
func isRepository(path string) bool {
	if st, err := os.Stat(path); err != nil || !st.IsDir() {
		return false
	}
	_, err := repo.Discover(path)
	return err == nil
}

// addedWorktree reports whether path is a worktree added to a
// repository that has a work tree of its own. One it cannot read is
// none: the repository is then kept, and the picker judges it again.
func addedWorktree(runner *run.Runner, path string) bool {
	loc, err := repo.Discover(path)
	if err != nil || !loc.Linked {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), addedTimeout)
	defer cancel()
	cfg, err := config.Read(ctx, runner, loc.GitDir)
	return err == nil && repo.Added(loc, cfg)
}

// addedTimeout bounds the reading of one repository's config.
const addedTimeout = 10 * time.Second

// Close closes the tab at i.
func (a *App) Close(i int) {
	if i < 0 || i >= len(a.tabs) {
		return
	}
	if a.noticeBy.tab == a.tabs[i] {
		a.dropNotice()
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

// CloseAll ends every session, stops a clone running and declines the
// questions from git left (those TakeAsks did not take), when the
// window closes.
func (a *App) CloseAll() {
	a.cancelAsks()
	for _, t := range a.tabs {
		t.s.Close()
	}
	a.tabs, a.active = nil, -1
	a.stopBriefs()
	// No frame comes to find the library statistics closed and stop
	// them: they would read on through the whole library.
	if s := &a.libStats; s.loader != nil {
		s.loader.Cancel()
		s.loading, s.loadedAt = false, ""
	}
	cs := &a.clone
	if cs.job != nil {
		cs.job.Cancel()
		cs.job = nil
	}
	cs.starting = false
	cs.gen++ // a clone still starting is stopped once it has
}

// Reload reads the current repository again, the views' kept answers
// dropped; the commit message being written stays.
func (a *App) Reload() {
	if t := a.current(); t != nil {
		a.reload(t)
	}
}

func (a *App) reload(t *tab) {
	t.s.Reload()
	t.dash.reload()
	t.pulls.key = ""
	t.commit.setup = nil
	t.cv.diff.key = diffKey{}
	t.links.For = nil
	t.locks.listed = -1
	t.pr.listAt = time.Time{}
	if rv := t.pr.rv; rv != nil {
		rv.reread()
	}
	for _, cv := range []*commitView{&t.hist.detail, &t.file.detail, &t.reflog.detail, &t.stash.detail} {
		cv.id = nil
	}
}

// WindowFocused reads every open repository again for what the watcher
// missed, unless the settings turn it off, and those not watched always.
func (a *App) WindowFocused() {
	for _, t := range a.tabs {
		// Other worktrees are not watched: what agents changed there is
		// read again when the dashboard shows next.
		t.dash.reload()
		// A repository not watched has nothing else to show changes by.
		if !a.Settings.NoFocusRefresh || t.s.State.WatchErr != nil {
			t.s.FocusRefresh()
		}
	}
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
	a.goWork(nil, func() {
		p, err := pick(locales.AppOpenRepository())
		update(func() {
			switch {
			case err != nil:
				a.notice = errText(err)
			case p != "":
				a.Open(p)
			}
		})
	})
}

// initAndOpen asks for a folder, makes it a repository, and opens it.
func (a *App) initAndOpen() {
	if a.PickFolder == nil || a.Runner == nil {
		return
	}
	pick, update, r, ts := a.PickFolder, a.Update, a.Runner, a.Trust
	a.goWork(nil, func() {
		p, err := pick(locales.AppNewRepository())
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
	})
}

// View builds the main window.
func (a *App) View(c *ui.Context) {
	// Before the theme: a setting changed since the last frame is in
	// force in this one.
	a.applySettings()
	a.themes.apply(c, a.Settings.Theme)
	a.claimNotice()
	a.find.expire()
	a.shortcuts(c)
	if a.clip != "" {
		c.WriteClipboard(a.clip)
		a.clip = ""
		c.AddToast(ui.Toast{ID: "copied", Title: locales.CommonCopied()})
	}
	for _, t := range a.toasts {
		c.AddToast(t)
	}
	a.toasts = a.toasts[:0]
	if a.openURL != "" {
		c.OpenURL(a.openURL)
		a.openURL = ""
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
	for _, t := range a.tabs {
		a.keepFetching(t)
		a.keepPanel(t)
	}
	root.Children(func() {
		if len(a.tabs) > 0 {
			a.tabBar(c)
			a.tabsStrip(c)
		}
		if t := a.current(); t != a.shownTab {
			if a.shownTab != nil && slices.Contains(a.tabs, a.shownTab) {
				a.hideTab(a.shownTab)
			}
			a.shownTab = t
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
	a.worktreeView(c)
	a.worktreeRemoveView(c)
	a.worktreeLockView(c)
	a.remoteCheckoutView(c)
	a.elsewhereView(c)
	a.askView(c) // last: over any other dialog
	a.messageView(c)
	a.historyEditView(c)
	if t := a.current(); t != nil {
		// Opened from the branches view or a commit's menu, and the
		// questions of a branch's menu, which the branch picker and the
		// sidebar open over any page.
		a.refDialogView(c, &t.refs.dlg, t)
		a.confirmView(c, &t.refs.ask)
		a.switchAsk(c, t)
	}
	a.stashDialogView(c)
	a.cleanView(c)
	a.mergeDialogView(c)
	a.compareView(c)
	a.mergeToolView(c)
	a.confirmView(c, &a.confirm)
	a.keysView(c)
	a.diagnosticsView(c)
	a.paletteView(c)
	a.noteLayout()
	a.countDialogs()
	a.tellRepoCommands()
}

// title is what a tab and the window call a repository.
func (a *App) title(t *tab) string {
	return filepath.Base(t.s.Path)
}

// Notify shows a message that belongs to no repository.
func (a *App) Notify(msg string) {
	msg = redact.Shown(msg)
	if !slices.Contains(a.windowNotes, msg) {
		a.windowNotes = append(a.windowNotes, msg)
	}
	a.notice = msg
	a.noticeBy = noticeOwner{seen: msg}
}

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
