package ui

import (
	"bytes"
	"encoding/hex"
	"strconv"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/diff"
	"ramus/internal/git/redact"
	"ramus/internal/git/refs"
	"ramus/internal/session"
)

// lineHistory is the history of a run of a file's lines, as git log -L
// follows them.
type lineHistory struct {
	// from and to are the lines asked for (1-based, inclusive) as the
	// work tree has them; 0 when none were.
	from, to int
	key      string
	keyFor   *refs.List
	loading  bool
	log      *session.LineLog
	err      string
	list     ui.ListState
	chosen   int
	shown    int // the commit whose patch res holds, -1 none
	res      session.PatchResult
	pv       patchView
	detail   float32
}

// linesMode is the file view's mode that shows a line history.
const linesMode = 2

// lineRange is the run of lines a menu on blame row i acts on: the lines
// chosen, when i is one of them, else i alone; 1-based.
func (fs *fileState) lineRange(i int) (from, to int) {
	if !fs.bpicked.Has(i) {
		return i + 1, i + 1
	}
	from, to = i, i
	for k := range fs.bpicked.All() {
		from, to = min(from, k), max(to, k)
	}
	return from + 1, to + 1
}

// lineHistoryItem offers the history of the lines blame row i stands for.
func (a *App) lineHistoryItem(m *ui.Menu, t *tab, i int) {
	fs := &t.file
	from, to := fs.lineRange(i)
	label := "History of This Line"
	if to > from {
		label = "History of These " + strconv.Itoa(to-from+1) + " Lines"
	}
	if m.Item(label).Chosen() {
		fs.lines = lineHistory{from: from, to: to, chosen: -1, shown: -1, detail: 560}
		fs.blame = linesMode
	}
}

// label names the lines a history follows.
func (lh *lineHistory) label() string {
	if lh.to == lh.from {
		return "Line " + strconv.Itoa(lh.from)
	}
	return "Lines " + strconv.Itoa(lh.from) + "–" + strconv.Itoa(lh.to)
}

func (a *App) loadLineHistory(t *tab) {
	fs, lh := &t.file, &t.file.lines
	key := fs.path + "\x00" + strconv.Itoa(lh.from) + "\x00" + strconv.Itoa(lh.to)
	if lh.loading || key == lh.key && t.s.State.Refs == lh.keyFor {
		return
	}
	lh.key, lh.keyFor, lh.loading = key, t.s.State.Refs, true
	t.s.LineLog(fs.path, lh.from, lh.to, func(l *session.LineLog, err error) {
		if lh.key != key {
			return
		}
		lh.loading = false
		var chosen []byte
		if lh.log != nil && lh.chosen >= 0 && lh.chosen < len(lh.log.Commits) {
			chosen = lh.log.Commits[lh.chosen].OID
		}
		lh.log, lh.err, lh.chosen, lh.shown = l, "", -1, -1
		if err != nil {
			lh.err = errText(err)
			return
		}
		for i, c := range l.Commits {
			if bytes.Equal(c.OID, chosen) {
				lh.chosen = i
			}
		}
		if lh.chosen < 0 && len(l.Commits) > 0 {
			lh.chosen = 0
		}
	})
}

func (a *App) lineHistoryView(c *ui.Context, t *tab) {
	th := c.Theme()
	fs, lh := &t.file, &t.file.lines
	a.loadLineHistory(t)
	switch {
	case lh.err != "":
		ui.Text(c, lh.err).TextColor(th.Danger).Padding(12).Selectable()
		return
	case lh.log == nil:
		ui.Column(c).Fill().Center().Children(func() { ui.Spinner(c) })
		return
	}
	l := lh.log
	if lh.chosen != lh.shown && lh.chosen >= 0 && lh.chosen < len(l.Commits) {
		lh.shown = lh.chosen
		p, err := diff.Parse(l.Patch(lh.chosen))
		lh.res = session.PatchResult{Parsed: p, Err: err}
		if err == nil {
			a.setPatch(&lh.pv, lh.res)
		}
	}
	ui.Split(c, &lh.detail, func() {
		ui.Column(c).Fill().Children(func() {
			if l.Limited {
				ui.Text(c, "The latest "+strconv.Itoa(session.MaxLineLog)+" commits").FontSize(12).TextColor(th.TextMuted).Padding(0, 12)
			}
			cs := l.Commits
			now := a.Now()
			lh.list.Selected = &lh.chosen
			lh.list.Label = func(i int) string {
				return redact.Control(cs[i].Subject) + ", " + redact.Control(cs[i].Author) + ", " + ago(now, cs[i].Time)
			}
			ui.List(c, &lh.list, len(cs), func(i int) {
				fc := cs[i]
				row := ui.Row(c).Padding(3, 12).Gap(8).AlignItems(ui.Center).FillWidth()
				row.Children(func() {
					ui.Text(c, redact.Control(fc.Subject)).SingleLine().Grow(1)
					if fc.Path != "" && fc.Path != fs.path {
						shown, _ := showPath(fc.Path)
						ui.Text(c, "as "+shown).FontSize(12).TextColor(th.TextMuted).SingleLine().MaxWidth(200)
					}
					ui.Text(c, redact.Control(fc.Author)).FontSize(12).Width(120).TextColor(th.TextMuted).SingleLine()
					ui.Text(c, ago(now, fc.Time)).FontSize(12).Width(88).TextColor(th.TextMuted).SingleLine()
					ui.Text(c, hex.EncodeToString(fc.OID)[:8]).Font("monospace").FontSize(12).Width(64).TextColor(th.TextMuted)
				})
				row.ContextMenu(func(m *ui.Menu) {
					if m.Item("Show in History").Chosen() {
						t.sel = "history"
						a.chooseCommit(t, fc.OID)
					}
					if m.Item("Copy Hash").Chosen() {
						a.clip = hex.EncodeToString(fc.OID)
					}
					a.commitActions(m, t, fc.OID)
				})
			}).Label("Commits that changed the lines").Grow(1)
		})
	}, func() {
		if lh.shown < 0 {
			return
		}
		a.patchBody(c, t, &lh.pv, lh.res, nil)
	}).SplitReset(560).Fill()
}
