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

// noticeOwner is whom App.notice belongs to. Most of the app raises a
// notice by setting App.notice where the user acted, in the tab shown;
// claimNotice gives it to that tab on the next frame, which keeps it as
// its own, so a notice of one tab never takes another's place. Work that
// ends later says its tab with tell, and Notify says the window.
type noticeOwner struct {
	// seen is the notice a tab was found for; tab is nil for none, the
	// welcome's.
	seen string
	tab  *tab
}

// tabNote is the notice of a tab.
type tabNote struct {
	text string
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
	a.noticeBy = noticeOwner{seen: a.notice, tab: t}
	if t != nil && a.notice != "" {
		t.note = tabNote{text: a.notice, after: lastEntry(t)}
	}
}

// tell raises msg as a notice of tab t, which no other tab shows.
func (a *App) tell(t *tab, msg string) {
	a.notice = msg
	a.noticeBy = noticeOwner{seen: msg, tab: t}
	if t != nil {
		t.note = tabNote{text: msg, after: lastEntry(t)}
	}
}

// dropNotice clears the notice no tab took, the welcome's.
func (a *App) dropNotice() {
	a.notice = ""
	a.noticeBy = noticeOwner{}
}

// dropTabNotice clears the notice of tab t.
func (a *App) dropTabNotice(t *tab) {
	if a.noticeBy.tab == t && a.notice == t.note.text {
		a.dropNotice()
	}
	t.note = tabNote{}
}

// dropWindowNote clears one of the window's notices.
func (a *App) dropWindowNote(msg string) {
	a.windowNotes = slices.DeleteFunc(a.windowNotes, func(n string) bool { return n == msg })
	if a.notice == msg && a.noticeBy.tab == nil {
		a.dropNotice()
	}
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
	if t.note.text == "" {
		return ""
	}
	log := t.s.Log
	for i := len(log) - 1; i >= 0 && log[i] != t.note.after; i-- {
		if e := log[i]; !e.Running && e.Err == nil && !e.Quiet {
			a.dropTabNotice(t)
			return ""
		}
	}
	return t.note.text
}

// failedWrite is the write or action of tab t that ended last, when it
// failed and the user has not dismissed it: one ending well after it
// clears it, and one the user stopped is no failure to tell of. A write
// the app made on its own is neither: it is tried again quietly.
func failedWrite(t *tab) *session.LogEntry {
	log := t.s.Log
	for i := len(log) - 1; i >= 0; i-- {
		e := log[i]
		switch {
		case e.Running, e.Quiet:
			continue
		case e.Err == nil || errors.Is(e.Err, context.Canceled) || e == t.failSeen:
			return nil
		}
		return e
	}
	return nil
}

// errorBar says what went wrong last in tab t: the window's notices, the
// tab's own and the write that failed, each until dismissed or a write
// ends well, with what can be done about it where the app knows. It is
// not there while all is well.
func (a *App) errorBar(c *ui.Context, t *tab) {
	notice, failed := a.tabNotice(t), failedWrite(t)
	var d diagnosis
	if failed != nil {
		d = a.diagnose(t, failed)
		if d.covered {
			failed = nil
		}
	}
	if notice == "" && failed == nil && len(a.windowNotes) == 0 {
		return
	}
	th := c.Theme()
	for i, n := range a.windowNotes {
		c.Key(windowKey(i))
		a.noteRow(c, noteLine{text: oneLine(n), full: n, color: th.Danger, dismiss: func() { a.dropWindowNote(n) }})
	}
	if notice != "" {
		c.Key("notice")
		a.noteRow(c, noteLine{text: oneLine(notice), full: notice, color: th.Danger, dismiss: func() { a.dropTabNotice(t) }})
	}
	if failed != nil {
		c.Key("failed")
		a.noteRow(c, noteLine{text: d.text, full: d.full, color: th.Danger, fixes: d.fixes,
			log: func() { a.showLogEntry(t, failed) }, dismiss: func() { t.failSeen = failed }})
	}
}

// noteLine is a message in a band: text, one line of it, in color,
// with the remedies of fixes, the whole message in full, the way to its
// command in the log, and to dismiss it, where there are.
type noteLine struct {
	text, full   string
	color        ui.Color
	fixes        []remedy
	log, dismiss func()
}

// noteRow is a message of the error bar, and of the welcome window: a
// line, with Details showing the whole of a message cut to it.
func (a *App) noteRow(c *ui.Context, n noteLine) ui.Element {
	th := c.Theme()
	return ui.Row(c).Padding(bannerPadY, insetX).Gap(spaceL).AlignItems(ui.Center).Background(th.Surface).Children(func() {
		ui.Text(c, n.text).FontSize(textSmall).TextColor(n.color).SingleLine().Shrink(1).Grow(1).Tooltip(n.full)
		for _, f := range n.fixes {
			if ui.Button(c, f.label).Clicked() {
				f.run()
			}
		}
		if full := strings.TrimSpace(n.full); full != "" && full != strings.TrimSpace(n.text) {
			a.noteDetails(c, full)
		}
		if n.log != nil && ui.Button(c, locales.RepoShowInLog()).Clicked() {
			n.log()
		}
		if n.dismiss != nil && closeButton(c, locales.RepoDismiss()).Clicked() {
			n.dismiss()
		}
	})
}

// noteDetails is the button that shows the whole of message full, to
// read, select and copy, which the line of it in a band cuts short.
func (a *App) noteDetails(c *ui.Context, full string) {
	b := ui.Button(c, locales.RepoNoticeDetails())
	if b.Clicked() {
		if a.noteShown == full {
			a.noteShown = ""
		} else {
			a.noteShown = full
		}
	}
	open := a.noteShown == full
	ui.Popover(c, b, &open, func() {
		ui.Column(c).Gap(spaceM).Padding(spaceS).MaxWidth(dialogMedium).Children(func() {
			ui.Scroll(c).MaxHeight(320).Children(func() {
				ui.Text(c, full).FontSize(textSmall).Selectable()
			})
			ui.Row(c).Justify(ui.End).Children(func() {
				if ui.Button(c, locales.RepoNoticeCopy()).Clicked() {
					a.clip = full
				}
			})
		})
	})
	if !open && a.noteShown == full {
		a.noteShown = ""
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

// failure is why a write failed, as git said it, without the command
// line the log already shows.
func failure(err error) string {
	var re *run.Error
	if errors.As(err, &re) {
		if s := strings.TrimSpace(re.Stderr); s != "" {
			return redact.Shown(s)
		}
		return errText(re.Err)
	}
	return errText(err)
}

// oneLine is the line of a message from git that says most, for a bar
// one line high: its first error, else its first line.
func oneLine(msg string) string {
	lines := strings.Split(msg, "\n")
	// git ends a failed push or fetch with a line that says nothing of
	// why ("failed to push some refs", "Could not read from remote
	// repository"); the cause is in the lines before it.
	for _, l := range lines {
		l = strings.TrimSpace(l)
		for _, m := range causeMarks {
			if strings.Contains(l, m) {
				for _, p := range []string{"remote: ", "fatal: ", "error: ", "! "} {
					l = strings.TrimPrefix(l, p)
				}
				return strings.Join(strings.Fields(l), " ")
			}
		}
	}
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

// windowKey keys the window's notice i among a bar's rows.
type windowKey int

// causeMarks are what a line naming a failure's cause holds: the refs a
// remote rejected, and ssh's and the server's words, which git does not
// translate.
var causeMarks = []string{
	"[rejected]",
	"[remote rejected]",
	"Host key verification failed",
	"REMOTE HOST IDENTIFICATION HAS CHANGED",
	"Permission denied (",
	"Authentication failed",
	"Invalid username or password",
	"hook declined",
}

// toast shows t on the next frame: what went well, briefly, with an
// action such as Undo. Failures stay in the error bar, which waits for
// the user.
func (a *App) toast(t ui.Toast) {
	a.toasts = append(a.toasts, t)
}
