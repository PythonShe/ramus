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
// CloseTab closes the tab shown, asking first as its close button does;
// false when there is none, for the window to close instead.
func (a *App) CloseTab() bool {
	if len(a.tabs) == 0 {
		return false
	}
	a.closeTab(a.active)
	return true
}

func (a *App) closeTab(i int) {
	if i < 0 || i >= len(a.tabs) {
		return
	}
	t := a.tabs[i]
	a.askToDrop(t, func() {
		// Tabs may have moved while it asked.
		if i := slices.Index(a.tabs, t); i >= 0 {
			a.Close(i)
		}
	})
}

// askToDrop runs close, which ends tab t, once the user agrees to stop
// the write running there and to lose the comments written in its pull
// request review and not sent; at once when there are neither.
func (a *App) askToDrop(t *tab, close func()) {
	n := t.unsentPR()
	if busyWrite(t) == nil && n == 0 {
		close()
		return
	}
	message, button := a.closingLoses(t), locales.AppStopAndClose()
	if busyWrite(t) == nil {
		button = locales.AppDiscardAndClose()
	}
	a.confirm = confirmState{open: true, title: locales.AppCloseTabTitle(a.title(t)), message: message, button: button, run: close}
}

// closingLoses says what closing tab t loses: the write it stops, and the
// comments of its review not sent.
func (a *App) closingLoses(t *tab) string {
	var parts []string
	if busyWrite(t) != nil {
		parts = append(parts, a.stillRunning(t))
	}
	if n := t.unsentPR(); n > 0 {
		parts = append(parts, locales.PrsUnsentLost(n, a.title(t)))
	}
	return strings.Join(parts, " ")
}

// AskToClose asks, while a write runs in one of the tabs or a clone
// runs, or a tab's pull request review holds comments not sent, whether
// to stop it, or lose them, and close the window, or quit the app with
// quit set; yes calls done on the main thread. It reports whether it
// asked: with nothing to lose it does not, and done is not called.
func (a *App) AskToClose(quit bool, done func()) bool {
	cloning := a.clone.job != nil || a.clone.starting
	// Every tab closes: each says what it loses. The one shown is the
	// first with a write running, else the first with comments.
	var parts []string
	shown, stops := a.busyTab(), cloning
	for _, t := range a.tabs {
		if busyWrite(t) == nil && t.unsentPR() == 0 {
			continue
		}
		if shown == nil {
			shown = t
		}
		stops = stops || busyWrite(t) != nil
		parts = append(parts, a.closingLoses(t))
	}
	if cloning {
		parts = append(parts, locales.AppCloneRunning(strings.TrimSpace(a.clone.url)))
	}
	if len(parts) == 0 {
		return false
	}
	if shown != nil {
		a.active = slices.Index(a.tabs, shown)
	}
	title, button := locales.AppCloseWindowTitle(), locales.AppStopAndClose()
	if quit {
		title, button = locales.AppQuitTitle(), locales.AppStopAndQuit()
	}
	if !stops {
		// Nothing to stop: only the comments are lost.
		button = locales.AppDiscardAndClose()
		if quit {
			button = locales.AppDiscardAndQuit()
		}
	}
	a.confirm = confirmState{open: true, title: title, message: strings.Join(parts, " "), button: button, run: done}
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
