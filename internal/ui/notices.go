package ui

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/redact"
	"ramus/internal/git/run"
	"ramus/internal/session"
	"ramus/locales"
)

// noticeOwner is whom the notice belongs to. Most of the app raises a
// notice by setting App.notice where the user acted, in the tab shown;
// claimNotice gives it to that tab on the next frame. Work that ends
// later says its tab with tell, and Notify says the window.
type noticeOwner struct {
	// seen is the notice tab was found for; tab is nil for the window,
	// whose notice every tab shows.
	seen string
	tab  *tab
	// after is the last entry of the tab's log when the notice came: a
	// write logged after it that ends well clears the notice.
	after *session.LogEntry
}

// claimNotice gives a notice set since the last frame to the tab shown.
func (a *App) claimNotice() {
	if a.notice == a.noticeBy.seen {
		return
	}
	t := a.current()
	a.noticeBy = noticeOwner{seen: a.notice, tab: t, after: lastEntry(t)}
}

// tell raises msg as a notice of tab t, which no other tab shows.
func (a *App) tell(t *tab, msg string) {
	a.notice = msg
	a.noticeBy = noticeOwner{seen: msg, tab: t, after: lastEntry(t)}
}

// dropNotice clears the notice.
func (a *App) dropNotice() {
	a.notice = ""
	a.noticeBy = noticeOwner{}
}

func lastEntry(t *tab) *session.LogEntry {
	if t == nil || len(t.s.Log) == 0 {
		return nil
	}
	return t.s.Log[len(t.s.Log)-1]
}

// tabNotice is the notice tab t shows, "" none. One a write ending well
// has made old since is dropped.
func (a *App) tabNotice(t *tab) string {
	o := a.noticeBy
	if a.notice == "" || o.tab != nil && o.tab != t {
		return ""
	}
	if o.tab == t {
		log := t.s.Log
		for i := len(log) - 1; i >= 0 && log[i] != o.after; i-- {
			if e := log[i]; !e.Running && e.Err == nil {
				a.dropNotice()
				return ""
			}
		}
	}
	return a.notice
}

// failedWrite is the write or action of tab t that ended last, when it
// failed and the user has not dismissed it: one ending well after it
// clears it, and one the user stopped is no failure to tell of.
func failedWrite(t *tab) *session.LogEntry {
	log := t.s.Log
	for i := len(log) - 1; i >= 0; i-- {
		e := log[i]
		switch {
		case e.Running:
			continue
		case e.Err == nil || errors.Is(e.Err, context.Canceled) || e == t.failSeen:
			return nil
		}
		return e
	}
	return nil
}

// errorBar says what went wrong last in tab t: the notice raised for it
// and the write that failed, each until dismissed or a write ends well.
// It is not there while all is well.
func (a *App) errorBar(c *ui.Context, t *tab) {
	notice, failed := a.tabNotice(t), failedWrite(t)
	if notice == "" && failed == nil {
		return
	}
	th := c.Theme()
	row := func(text, full string, open bool, dismiss func()) {
		ui.Row(c).Padding(bannerPadY, insetX).Gap(spaceL).AlignItems(ui.Center).Background(th.Surface).Children(func() {
			ui.Text(c, text).FontSize(textSmall).TextColor(th.Danger).SingleLine().Shrink(1).Grow(1).Tooltip(full)
			if open && ui.Button(c, locales.RepoShowInLog()).Clicked() {
				a.showLogEntry(t, failed)
			}
			if closeButton(c, locales.RepoDismiss()).Clicked() {
				dismiss()
			}
		})
	}
	if notice != "" {
		c.Key("notice")
		row(oneLine(notice), notice, false, a.dropNotice)
	}
	if failed != nil {
		why := failure(failed.Err)
		c.Key("failed")
		row(locales.RepoWriteFailed(failed.Name, oneLine(why)), why, true, func() { t.failSeen = failed })
	}
}

// showLogEntry shows entry e in the command log of tab t.
func (a *App) showLogEntry(t *tab, e *session.LogEntry) {
	i := slices.Index(t.s.Log, e)
	if i < 0 {
		return
	}
	t.sel, t.gitLog.mode = "log", 0
	t.logChosen = len(t.s.Log) - 1 - i // newest first
}

// failure is why a write failed, as git said it without the command
// line the log already shows.
func failure(err error) string {
	var re *run.Error
	if errors.As(err, &re) {
		if s := strings.TrimSpace(re.Stderr); s != "" {
			return redact.Shown(s)
		}
	}
	return errText(err)
}

// oneLine is the line of a message from git that says most, for a bar
// one line high: its first error, else its first line.
func oneLine(msg string) string {
	lines := strings.Split(msg, "\n")
	for _, l := range lines {
		for _, p := range []string{"fatal: ", "error: "} {
			if rest, ok := strings.CutPrefix(strings.TrimSpace(l), p); ok {
				return rest
			}
		}
	}
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}
