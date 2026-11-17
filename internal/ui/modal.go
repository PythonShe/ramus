package ui

import "github.com/egoist/mygo/ui"

// The window's own shortcuts wait while a mygo Modal shows, but the app
// menu takes its keys before the window and mygo tells no one a dialog is
// up. Every dialog of the app is built through modal or dialogBase, which
// count it, so that the menu's commands wait too: a Cmd+Enter meant for a
// dialog's text must not commit what is staged behind it.

// modal shows a dialog as ui.Modal does, counting it as open.
func (a *App) modal(c *ui.Context, open *bool, fn func()) ui.Element {
	if *open {
		a.modals++
	}
	return ui.Modal(c, open, fn)
}

// dialogBase shows a dialog as ui.DialogBase does, counting it as open.
func (a *App) dialogBase(c *ui.Context, open *bool, fn func(back, panel ui.Element)) ui.Element {
	if *open {
		a.modals++
	}
	return ui.DialogBase(c, open, fn)
}

// countDialogs ends a frame's count of the dialogs it showed.
func (a *App) countDialogs() {
	a.modalUp, a.modals = a.modals > 0, 0
}

// DialogOpen reports whether the last frame showed a dialog of the
// window, which then has the keys: the app menu's commands wait.
func (a *App) DialogOpen() bool { return a.modalUp }
