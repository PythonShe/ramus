package ui

import (
	"slices"
	"strings"

	"ramus/internal/session"
	"ramus/locales"
)

// busyWrite is the write or action running in tab t, or the first of
// those queued, nil none: closing the tab kills git part way through it,
// which can leave index.lock or a rebase half done. A write the app made
// on its own is not asked about: it stops cleanly, and is made again.
func busyWrite(t *tab) *session.LogEntry {
	for _, e := range t.s.Log {
		if e.Running && !e.Quiet {
			return e
		}
	}
	return nil
}

// busyTab is the first tab with a write running, nil none.
func (a *App) busyTab() *tab {
	for _, t := range a.tabs {
		if busyWrite(t) != nil {
			return t
		}
	}
	return nil
}

// stillRunning says what closing tab t would stop.
func (a *App) stillRunning(t *tab) string {
	name := ""
	if e := busyWrite(t); e != nil {
		name = e.Name
	}
	return locales.AppStillRunning(name, a.title(t))
}

// closeTab closes the tab at i as the user asked, first asking whether
// to stop the write running there.
func (a *App) closeTab(i int) {
	if i < 0 || i >= len(a.tabs) {
		return
	}
	t := a.tabs[i]
	if busyWrite(t) == nil {
		a.Close(i)
		return
	}
	a.confirm = confirmState{open: true, title: locales.AppCloseTabTitle(a.title(t)), message: a.stillRunning(t),
		button: locales.AppStopAndClose(), run: func() {
			// Tabs may have moved while it asked.
			if i := slices.Index(a.tabs, t); i >= 0 {
				a.Close(i)
			}
		}}
}

// AskToClose asks, while a write runs in one of the tabs or a clone
// runs, whether to stop it and close the window, or quit the app with quit set; yes calls done
// on the main thread. It reports whether it asked: with nothing running
// it does not, and done is not called.
func (a *App) AskToClose(quit bool, done func()) bool {
	t := a.busyTab()
	cloning := a.clone.job != nil || a.clone.starting
	if t == nil && !cloning {
		return false
	}
	var message string
	if t != nil {
		a.active = slices.Index(a.tabs, t)
		message = a.stillRunning(t)
	} else {
		message = locales.AppCloneRunning(strings.TrimSpace(a.clone.url))
	}
	title, button := locales.AppCloseWindowTitle(), locales.AppStopAndClose()
	if quit {
		title, button = locales.AppQuitTitle(), locales.AppStopAndQuit()
	}
	a.confirm = confirmState{open: true, title: title, message: message, button: button, run: done}
	return true
}

// StopWrites stops the writes running and queued in every tab, and a
// clone, before the window closes or the app quits; Writing reports when
// they have ended.
func (a *App) StopWrites() {
	for _, t := range a.tabs {
		for _, e := range t.s.Log {
			e.Cancel()
		}
	}
	if j := a.clone.job; j != nil {
		j.Cancel()
	}
	if a.clone.starting {
		// Its folder is still being checked: the job, once made, is
		// cancelled as one stopped.
		a.clone.starting = false
		a.clone.gen++
	}
}

// Writing reports a write or a clone still running.
func (a *App) Writing() bool { return a.busyTab() != nil || a.clone.job != nil }
