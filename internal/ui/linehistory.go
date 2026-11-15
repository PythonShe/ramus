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
	"ramus/locales"
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
	list     tableState
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
	if m.Item(locales.FileHistoryLinesItem(to - from + 1)).Chosen() {
		fs.lines = lineHistory{from: from, to: to, chosen: -1, shown: -1, detail: 560}
		fs.blame = linesMode
	}
}

// label names the lines a history follows.
func (lh *lineHistory) label() string {
	if lh.to == lh.from {
		return locales.FileHistoryLine(lh.from)
	}
	return locales.FileHistoryLines(lh.from, lh.to)
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
		errorMessage(c, lh.err)
		return
	case lh.log == nil:
		loading(c, "")
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
				ui.Text(c, locales.FileHistoryLatest(session.MaxLineLog)).FontSize(textSmall).TextColor(th.TextMuted).Padding(0, insetX)
			}
			cs := l.Commits
			now := a.Now()
			lh.list.Selected = &lh.chosen
			lh.list.Label = func(i int) string {
				return locales.FileHistoryCommit(redact.Control(cs[i].Subject), redact.Control(cs[i].Author), ago(now, cs[i].Time))
			}
			lh.list.CellMenu = func(m *ui.Menu, i, _ int) {
				fc := cs[i]
				if m.Item(locales.FileHistoryShowInHistory()).Chosen() {
					t.sel = "history"
					a.chooseCommit(t, fc.OID)
				}
				if m.Item(locales.CommonCopyHash()).Chosen() {
					a.clip = hex.EncodeToString(fc.OID)
				}
				a.commitActions(m, t, fc.OID)
			}
			a.savedTable(c, "lineHistory", &lh.list, commitColumns(), len(cs), func(i, col int) {
				fileCommitCell(c, lh.list.ink(c, i), cs[i], fs.path, now, col)
			}).Label(locales.FileHistoryLineCommits()).Grow(1)
		})
	}, func() {
		if lh.shown < 0 {
			return
		}
		a.patchBody(c, t, &lh.pv, lh.res, nil)
	}).SplitReset(560).Fill()
}
