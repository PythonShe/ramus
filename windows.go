package main

import (
	"slices"

	"github.com/egoist/mygo"
	mygoui "github.com/egoist/mygo/ui"

	"ramus/internal/git/run"
	"ramus/internal/settings"
	"ramus/internal/ui"
)

// window is one window of repositories, with its own tabs.
type window struct {
	win *mygo.Window
	app *ui.App
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
}

// add makes a window's app; show opens its window once the app is
// ready.
func (ws *windows) add() *window {
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
	w.app = ui.NewApp(d)
	w.app.KeysChanged = func() { mygo.App.SetMenu(menu(ws)) }
	if ws.found {
		w.app.SetRunner(ws.git, ws.gitErr)
	}
	ws.list = append(ws.list, w)
	if ws.focused == nil {
		ws.focused = w
	}
	return w
}

// show opens w's window: the first one where it was last, the others
// where the system puts them.
func (ws *windows) show(w *window) {
	opts := mygo.WindowOptions{
		Title:     "Ramus",
		Width:     1200,
		Height:    800,
		MinWidth:  720,
		MinHeight: 480,
		Content:   mygoui.View(w.app.View),
	}
	if len(ws.list) == 1 {
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
	w.win.OnClosed(func() {
		w.app.CloseAll()
		ws.list = slices.DeleteFunc(ws.list, func(o *window) bool { return o == w })
		if ws.focused == w {
			ws.focused = nil
			if n := len(ws.list); n > 0 {
				ws.focused = ws.list[n-1]
			}
		}
	})
	w.win.OnFocus(func() {
		ws.focused = w
		w.app.WindowFocused()
	})
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
