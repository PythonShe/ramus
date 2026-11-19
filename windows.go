package main

import (
	"log"
	"maps"
	"slices"
	"time"

	"github.com/egoist/mygo"
	mygoui "github.com/egoist/mygo/ui"

	"ramus/internal/askpass"
	"ramus/internal/git/run"
	"ramus/internal/layout"
	"ramus/internal/settings"
	"ramus/internal/ui"
)

// window is one window of repositories, with its own tabs.
type window struct {
	win *mygo.Window
	app *ui.App
	// closing is set once the user said to close it, a write running.
	closing bool
}

// windows are the app's windows. They share the stores, the settings
// and git, and are read and changed on the main thread only.
type windows struct {
	deps  ui.Deps
	probe string
	ready bool
	list  []*window
	// focused is the window last focused, nil when none is open.
	focused *window
	git     *run.Runner
	gitErr  error
	found   bool
	// keep writes what the windows have open, nil keeps nothing; saving
	// is a write coming; quitting is set from the start of a quit to
	// its end, when the layout is written whole and windows closing do
	// not change it.
	keep     *layout.Keeper
	saving   bool
	quitting bool
	// quitAnyway is set when the user said to quit, a write running.
	quitAnyway bool
	// about is the About window, nil while it is not open.
	about *aboutWindow
	// watching is set once the settings file is watched for edits.
	watching bool
}

// layoutDelay is how long changes to what is open gather before the
// layout is written: a write per frame of a drag is too many.
const layoutDelay = time.Second

// layout is what the windows have open, the first window first.
func (ws *windows) layout() []layout.Window {
	out := make([]layout.Window, 0, len(ws.list))
	for _, w := range ws.list {
		out = append(out, w.app.Layout())
	}
	return out
}

// layoutChanged has the layout written soon.
func (ws *windows) layoutChanged() {
	// Until git is found the tabs asked for are not open yet: the
	// layout read at launch stays.
	if ws.keep == nil || !ws.found || ws.gitErr != nil || ws.saving || ws.quitting {
		return
	}
	ws.saving = true
	time.AfterFunc(layoutDelay, func() {
		mygo.RunOnMain(func() {
			ws.saving = false
			// The last window closing quits the app; what it had open is
			// what opens next time, so it is not written away.
			if !ws.quitting && len(ws.list) > 0 {
				ws.keep.Keep(ws.layout())
			}
		})
	})
}

// flush writes the layout and the drafts, and waits for the settings
// and the git identity typed in them being written, at the start of a
// quit. They are the writes the main
// thread waits on: the OS may end the process as soon as the quit's
// listeners return, and the files are small.
func (ws *windows) flush() {
	if k := ws.deps.SettingsKeeper; k != nil {
		k.Wait()
	}
	for _, w := range ws.list {
		w.app.SaveIdentity()
		w.app.WaitIdentity()
	}
	if d := ws.deps.Drafts; d != nil {
		if err := d.Flush(); err != nil {
			log.Print(err)
		}
	}
	if ws.keep == nil || !ws.found || ws.gitErr != nil || len(ws.list) == 0 {
		return
	}
	if err := ws.keep.Flush(ws.layout()); err != nil {
		log.Print(err)
	}
}

// watchSettings has the settings file, which the user may edit while the
// app runs, read again when a window gains focus and before each save,
// and what was edited put in force in every window.
func (ws *windows) watchSettings() {
	k := ws.deps.SettingsKeeper
	if k == nil || ws.probe != "" || ws.watching {
		return
	}
	ws.watching = true
	k.Started(ws.deps.Settings)
	k.OnFile = func(f settings.FromFile) {
		mygo.RunOnMain(func() { ws.settingsFromFile(f) })
	}
}

// checkSettings reads the settings file again, off the main thread.
func (ws *windows) checkSettings() {
	if k := ws.deps.SettingsKeeper; k != nil && ws.watching {
		k.Check(func(err error) {
			mygo.RunOnMain(func() {
				if w := ws.focused; w != nil {
					w.app.Notify(ui.LoadNotice(err))
					if w.win != nil {
						w.win.Invalidate()
					}
				}
			})
		})
	}
}

// settingsFromFile puts in force what the settings file says, edited
// by hand, merged with what the app changed since it read it.
func (ws *windows) settingsFromFile(f settings.FromFile) {
	old := ws.deps.Settings
	s, changed := ws.deps.SettingsKeeper.Take(f, old)
	if !changed {
		return
	}
	ws.deps.Settings = s
	if s.Theme != old.Theme && ws.deps.ApplyTheme != nil {
		ws.deps.ApplyTheme(s.Theme)
	}
	for _, w := range ws.list {
		w.app.UseSettings(s)
		if w.win != nil {
			w.win.Invalidate()
		}
	}
	if !maps.EqualFunc(s.Keys, old.Keys, slices.Equal) && ws.ready {
		mygo.App.SetMenu(menu(ws))
	}
}

// add makes a window's app; show opens its window once the app is
// ready.
func (ws *windows) add() *window {
	ws.watchSettings()
	w := &window{}
	d := ws.deps
	d.Update = func(fn func()) {
		mygo.RunOnMain(func() {
			if w.win == nil {
				fn() // no view yet to rebuild
				return
			}
			w.win.Update(fn)
		})
	}
	d.PickFolder = func(title string) (string, error) {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: w.win, Title: title, Directory: true, CreateDirectories: true})
		if err != nil || len(paths) == 0 {
			return "", err
		}
		return paths[0], nil
	}
	d.PickFile = func(title string) (string, error) {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: w.win, Title: title})
		if err != nil || len(paths) == 0 {
			return "", err
		}
		return paths[0], nil
	}
	d.SaveFile = func(title, name string) (string, error) {
		return mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: w.win, Title: title, DefaultPath: name, CreateDirectories: true})
	}
	if ws.probe == "" {
		d.NewWindow = ws.open
		d.ShowAbout = ws.showAbout
	}
	d.SettingsChanged = func(s settings.Settings) {
		ws.deps.Settings = s
		for _, o := range ws.list {
			if o != w {
				o.app.UseSettings(s)
				if o.win != nil {
					o.win.Invalidate()
				}
			}
		}
	}
	d.ShowOpen = func(p string) bool {
		for _, o := range ws.list {
			if o != w && o.app.Has(p) {
				o.app.Open(p)
				ws.raise(o)
				return true
			}
		}
		return false
	}
	d.LayoutChanged = ws.layoutChanged
	d.Languages, d.ChosenLocale, d.SetLocale = mygo.App.Languages(), mygo.App.ChosenLocale, mygo.App.SetLocale
	w.app = ui.NewApp(d)
	w.app.KeysChanged = func() { mygo.App.SetMenu(menu(ws)) }
	w.app.RepoCommandsChanged = func(bool) {
		if w == ws.focused {
			ws.repoMenuEnabled()
		}
	}
	if ws.found {
		w.app.SetRunner(ws.git, ws.gitErr)
	}
	ws.list = append(ws.list, w)
	if ws.focused == nil {
		ws.focused = w
	}
	return w
}

// The least size of a window: room for the sidebar beside a list, a
// commit and its patch, each at the least width the views give it.
const (
	minWindowW = 960
	minWindowH = 600
)

// show opens w's window: the first one where it was last, the others
// where the system puts them.
func (ws *windows) show(w *window) {
	opts := mygo.WindowOptions{
		Title:     "Ramus",
		Width:     1200,
		Height:    800,
		MinWidth:  minWindowW,
		MinHeight: minWindowH,
		Content:   mygoui.View(w.app.View),
	}
	if w == ws.list[0] {
		opts.StateKey = "main"
	}
	if ws.probe != "" {
		opts.Hidden, opts.StateKey = true, ""
	}
	w.win = mygo.NewWindow(opts)
	if ws.probe != "" {
		probeMark("window")
		probeWindow(w.win, ws.probe)
	}
	w.win.OnClose(func(e *mygo.CloseEvent) {
		if ws.quitting || w.closing {
			return
		}
		if w.app.AskToClose(false, func() {
			stopThen([]*ui.App{w.app}, func() {
				w.closing = true
				w.win.Close()
			})
		}) {
			e.PreventDefault()
			w.win.Invalidate()
		}
	})
	w.win.OnClosed(func() {
		if len(ws.list) == 1 && !ws.quitting {
			// What the last window had open is what opens next time.
			ws.flush()
			// The app quits with its last window, the About window
			// included: while it stays open, windows opened later are
			// asked about and kept as any other.
			if ws.about == nil {
				ws.quitting = true
			}
		}
		w.app.SaveIdentity() // the settings open, the change typed is kept
		asks := w.app.TakeAsks()
		w.app.CloseAll()
		ws.list = slices.DeleteFunc(ws.list, func(o *window) bool { return o == w })
		if ws.focused == w {
			ws.focused = nil
			if n := len(ws.list); n > 0 {
				ws.focused = ws.list[n-1]
			}
		}
		for _, q := range asks {
			ws.ask(q)
		}
		ws.layoutChanged()
	})
	w.win.OnFocus(func() {
		ws.focused = w
		ws.checkSettings()
		w.app.WindowFocused()
		ws.repoMenuEnabled()
	})
}

// beforeQuit asks, while a write runs in a window, whether to stop it
// and quit, and reports whether it asked; the quit then waits for the
// answer. The OS ending the session is not asked about.
func (ws *windows) beforeQuit(e *mygo.QuitEvent) bool {
	if e.SessionEnding || ws.quitAnyway {
		return false
	}
	for _, w := range ws.list {
		if w.win != nil && w.app.AskToClose(true, func() {
			ws.quitAnyway = true
			apps := make([]*ui.App, len(ws.list))
			for i, o := range ws.list {
				apps[i] = o.app
			}
			stopThen(apps, mygo.App.Quit)
		}) {
			ws.raise(w)
			return true
		}
	}
	return false
}

// stopWait bounds how long closing waits for the writes it stopped to
// end; git, told to stop, has as long to take its locks away (run's
// WaitDelay) before it is killed.
const stopWait = 3 * time.Second

// stopPoll is how often stopThen looks whether the writes have ended.
const stopPoll = 50 * time.Millisecond

// stopThen stops the writes running in apps and calls done on the main
// thread once they have ended, or after stopWait: closing the window or
// quitting at once could end the app before git had stopped. The main
// thread stays free meanwhile, for the writes' ends to reach it.
func stopThen(apps []*ui.App, done func()) {
	for _, a := range apps {
		a.StopWrites()
	}
	deadline := time.Now().Add(stopWait)
	var check func()
	check = func() {
		if time.Now().Before(deadline) && slices.ContainsFunc(apps, (*ui.App).Writing) {
			time.AfterFunc(stopPoll, func() { mygo.RunOnMain(check) })
			return
		}
		done()
	}
	// Not now: the frame that asked is still being built.
	time.AfterFunc(0, func() { mygo.RunOnMain(check) })
}

// open is a new window with the repositories at paths; one already open
// in another window is shown there instead, so that no repository has
// two windows writing to it.
func (ws *windows) open(paths []string) {
	var rest []string
	for _, p := range paths {
		if o := ws.showing(p); o != nil {
			o.app.Open(p)
			ws.raise(o)
		} else {
			rest = append(rest, p)
		}
	}
	if len(paths) > 0 && len(rest) == 0 {
		return
	}
	w := ws.add()
	for _, p := range rest {
		w.app.Open(p)
	}
	if ws.ready {
		ws.show(w)
		w.win.Focus()
	}
}

// showing is the window with the repository at path open, nil none.
func (ws *windows) showing(path string) *window {
	for _, w := range ws.list {
		if w.app.Has(path) {
			return w
		}
	}
	return nil
}

// ask shows a question from git or ssh in the window of the repository
// that asks, else the one last focused. During a quit, or with no window
// left, it is declined rather than open a window.
func (ws *windows) ask(q *askpass.Ask) {
	if ws.quitting || len(ws.list) == 0 {
		q.Cancel()
		return
	}
	w := ws.showing(q.Dir)
	if w == nil {
		w = ws.target()
	}
	w.app.Ask(q)
	if w.win != nil {
		w.win.Invalidate()
	}
}

// target is the window a request goes to: the one last focused, or a
// new one when every window was closed.
func (ws *windows) target() *window {
	if ws.focused != nil {
		return ws.focused
	}
	w := ws.add()
	if ws.ready {
		ws.show(w)
	}
	return w
}

func (ws *windows) raise(w *window) {
	if w.win != nil {
		w.win.Invalidate()
		w.win.Restore()
		w.win.Focus()
	}
}

// of is the window that shows win, nil none.
func (ws *windows) of(win *mygo.Window) *window {
	for _, w := range ws.list {
		if w.win == win {
			return w
		}
	}
	return nil
}
