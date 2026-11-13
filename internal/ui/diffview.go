package ui

import (
	"encoding/hex"
	"fmt"
	"strconv"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/diff"
	"ramus/internal/git/redact"
	"ramus/internal/git/status"
	"ramus/internal/git/write"
	"ramus/internal/session"
)

// MaxShownPatch bounds the patch the changes view reads for one file.
const MaxShownPatch = 32 << 20

// diffKey is what a shown patch was read for; a new status reads it again.
type diffKey struct {
	path string
	src  diff.Source
	st   *status.Status
}

// diffState is the patch of the file chosen in the changes list.
type diffState struct {
	key     diffKey
	loading bool
	res     session.PatchResult
	// rows maps the list's rows to the patch: a line index, or -1-h for
	// the header of hunk h.
	rows   []int32
	list   ui.ListState
	picked ui.Selection[int32]
	chosen int
}

func newDiffState() diffState { return diffState{chosen: -1} }

// chosenFile is the file row chosen in the changes list, if any.
func (cv *changesState) chosenFile() (changeRow, bool) {
	m := cv.model
	if m == nil || cv.chosen < 0 || cv.chosen >= len(m.rows) || m.rows[cv.chosen].kind != rowFile {
		return changeRow{}, false
	}
	return m.rows[cv.chosen], true
}

func (a *App) detail(c *ui.Context, t *tab) {
	th := c.Theme()
	cv := &t.cv
	r, ok := cv.chosenFile()
	if !ok {
		ui.Column(c).Fill().Center().Children(func() {
			ui.Text(c, "Choose a file to see its changes").TextColor(th.TextMuted)
		})
		return
	}
	m := cv.model
	i := int(r.idx)
	key := diffKey{path: string(m.st.Path(i)), src: diff.Unstaged, st: m.st}
	switch {
	case r.sec == secStaged:
		key.src = diff.Staged
	case m.st.Kind(i) == status.Untracked:
		key.src = diff.Untracked
	}
	d := &cv.diff
	if d.key != key {
		if d.key.path != key.path || d.key.src != key.src {
			d.picked.Clear()
			d.list = ui.ListState{}
			d.chosen = -1
		}
		d.key, d.loading = key, true
		t.s.Patch(diff.PatchRequest{Source: key.src, Paths: []string{key.path}, Context: -1, MaxBytes: MaxShownPatch}, func(res session.PatchResult) {
			if d.key != key {
				return
			}
			d.loading, d.res, d.rows = false, res, patchRows(res.Parsed)
		})
	}
	ui.Column(c).Fill().Children(func() {
		a.diffHeader(c, t, m, r)
		switch {
		case d.loading && d.res.Parsed == nil:
			ui.Column(c).Fill().Center().Children(func() { ui.Spinner(c) })
			return
		case d.res.Err != nil:
			ui.Text(c, errText(d.res.Err)).TextColor(th.Danger).Padding(12).Selectable()
			return
		}
		p := d.res.Parsed
		if p == nil || len(p.Files) == 0 {
			ui.Column(c).Fill().Center().Children(func() {
				ui.Text(c, "No differences in content").TextColor(th.TextMuted)
			})
			return
		}
		if p.Files[0].Binary {
			ui.Column(c).Fill().Center().Children(func() {
				ui.Text(c, "Binary file").TextColor(th.TextMuted)
			})
			return
		}
		if d.res.Truncated || p.Cut {
			ui.Text(c, fmt.Sprintf("Only the first %d MB of this patch are shown; lines cannot be staged from it", MaxShownPatch>>20)).
				FontSize(12).TextColor(th.Warning).Padding(4, 12)
		}
		a.patchList(c, t, p, r)
	})
}

// patchRows lays out a parsed patch of one file: each hunk's header, then
// its lines.
func patchRows(p *diff.Parsed) []int32 {
	if p == nil || len(p.Files) == 0 {
		return nil
	}
	f := p.Files[0]
	var rows []int32
	for h := f.Hunks.Start; h < f.Hunks.End; h++ {
		rows = append(rows, -1-int32(h))
		for l := p.Hunks[h].Lines.Start; l < p.Hunks[h].Lines.End; l++ {
			rows = append(rows, int32(l))
		}
	}
	return rows
}

// partialOK reports whether lines of this patch can be staged, unstaged
// or discarded one by one.
func (d *diffState) partialOK() bool {
	p := d.res.Parsed
	return p != nil && len(p.Files) == 1 && !p.Files[0].Binary && !p.Files[0].Quoted &&
		!d.res.Truncated && !p.Cut && d.key.src != diff.Untracked
}

func (a *App) diffHeader(c *ui.Context, t *tab, m *changeModel, r changeRow) {
	th := c.Theme()
	d := &t.cv.diff
	ro := t.s.Queue == nil
	ui.Row(c).Padding(6, 12).Gap(8).AlignItems(ui.Center).Children(func() {
		shown, _ := showPath(string(m.st.Path(int(r.idx))))
		ui.Text(c, shown).Bold().SingleLine().Grow(1)
		ui.Text(c, entryWords(m.st, int(r.idx), r.sec)).FontSize(12).TextColor(th.TextMuted)
		n := d.pickedLines()
		if !d.partialOK() || n == 0 {
			return
		}
		lines := fmt.Sprintf(" %d Lines", n)
		if n == 1 {
			lines = " 1 Line"
		}
		if r.sec == secStaged {
			if ui.Button(c, "Unstage"+lines).Disabled(ro).Clicked() {
				a.applyLines(t, d.pickedSet(), true, true)
			}
			return
		}
		if ui.Button(c, "Discard"+lines+"…").Disabled(ro).Clicked() {
			a.askDiscardLines(t, d.pickedSet())
		}
		if ui.Button(c, "Stage"+lines).Disabled(ro).Clicked() {
			a.applyLines(t, d.pickedSet(), true, false)
		}
	})
}

// pickedLines counts the changed lines picked.
func (d *diffState) pickedLines() int {
	p := d.res.Parsed
	if p == nil {
		return 0
	}
	n := 0
	for k := range d.picked.All() {
		if k >= 0 && int(k) < len(p.Lines) && (p.Lines[k].Kind == '+' || p.Lines[k].Kind == '-') {
			n++
		}
	}
	return n
}

// pickedSet is the lines picked, as Select takes them.
func (d *diffState) pickedSet() func(int) bool {
	set := map[int]bool{}
	for k := range d.picked.All() {
		set[int(k)] = true
	}
	return func(i int) bool { return set[i] }
}

func hunkSet(p *diff.Parsed, h int32) func(int) bool {
	lines := p.Hunks[h].Lines
	return func(i int) bool { return uint32(i) >= lines.Start && uint32(i) < lines.End }
}

func (a *App) patchList(c *ui.Context, t *tab, p *diff.Parsed, r changeRow) {
	th := c.Theme()
	d := &t.cv.diff
	rows := d.rows
	partial := d.partialOK()
	ro := t.s.Queue == nil
	d.list.Key = func(i int) any { return rows[i] }
	d.list.Selected = &d.chosen
	if partial {
		d.list.Selection = &d.picked
	} else {
		d.list.Selection = nil
	}
	d.list.Header = func(i int) bool { return rows[i] < 0 }
	d.list.Label = func(i int) string {
		if k := rows[i]; k < 0 {
			return string(p.Bytes(p.Hunks[-1-k].Header))
		} else {
			return lineLabel(p, p.Lines[k])
		}
	}
	var act func()
	ui.List(c, &d.list, len(rows), func(i int) {
		k := rows[i]
		if k < 0 {
			h := -1 - k
			ui.Row(c).Padding(4, 12).Gap(8).AlignItems(ui.Center).FillWidth().Background(th.Surface).Children(func() {
				ui.Text(c, redact.Control(string(p.Bytes(p.Hunks[h].Header)))).Font("monospace").FontSize(12).TextColor(th.TextMuted).SingleLine().Grow(1)
				if !partial {
					return
				}
				if r.sec == secStaged {
					if ui.Button(c, "Unstage Hunk").Disabled(ro).Clicked() {
						act = func() { a.applyLines(t, hunkSet(p, h), true, true) }
					}
					return
				}
				if ui.Button(c, "Discard Hunk…").Disabled(ro).Clicked() {
					act = func() { a.askDiscardLines(t, hunkSet(p, h)) }
				}
				if ui.Button(c, "Stage Hunk").Disabled(ro).Clicked() {
					act = func() { a.applyLines(t, hunkSet(p, h), true, false) }
				}
			})
			return
		}
		patchLine(c, p, p.Lines[k])
	}).Grow(1)
	if act != nil {
		act()
	}
}

// patchLine is one line of a patch: its old and new numbers and text, on
// green when added and red when removed.
func patchLine(c *ui.Context, p *diff.Parsed, l diff.Line) {
	th := c.Theme()
	bg := ui.Color{}
	switch l.Kind {
	case '+':
		bg = th.Success.Alpha(0.14)
	case '-':
		bg = th.Danger.Alpha(0.14)
	}
	ui.Row(c).Padding(0, 12).Gap(8).FillWidth().Background(bg).Children(func() {
		num := func(n int32) string {
			if n == 0 {
				return ""
			}
			return strconv.Itoa(int(n))
		}
		ui.Text(c, num(l.Old)).Font("monospace").FontSize(12).TextColor(th.TextMuted).Width(44).TextAlign(ui.End)
		ui.Text(c, num(l.New)).Font("monospace").FontSize(12).TextColor(th.TextMuted).Width(44).TextAlign(ui.End)
		text := string(l.Kind) + redact.Control(string(p.Bytes(l.Text)))
		if l.Kind == '\\' {
			text = "\\" + string(p.Bytes(l.Text))
		}
		ui.Text(c, text).Font("monospace").FontSize(12).SingleLine().Grow(1)
	})
}

func lineLabel(p *diff.Parsed, l diff.Line) string {
	text := redact.Control(string(p.Bytes(l.Text)))
	switch l.Kind {
	case '+':
		return "added line " + strconv.Itoa(int(l.New)) + ": " + text
	case '-':
		return "removed line " + strconv.Itoa(int(l.Old)) + ": " + text
	case '\\':
		return "no newline at end of file"
	}
	return "line " + strconv.Itoa(int(l.New)) + ": " + text
}

// applyLines stages (cached, not reverse), unstages (cached, reverse) or
// discards (reverse) the chosen lines of the patch shown.
func (a *App) applyLines(t *tab, chosen func(int) bool, cached, reverse bool) {
	op, err := a.linesOp(t, chosen, cached, reverse)
	if err == nil {
		_, err = t.s.Submit(op)
	}
	if err != nil {
		a.notice = errText(err)
	}
	t.cv.diff.picked.Clear()
}

func (a *App) linesOp(t *tab, chosen func(int) bool, cached, reverse bool) (write.Op, error) {
	d := &t.cv.diff
	patch, err := d.res.Parsed.Select(0, reverse, chosen)
	if err != nil {
		return write.Op{}, err
	}
	return write.Apply(t.s.Where(), write.Partial{Path: d.key.path, Patch: patch, Cached: cached, Reverse: reverse}), nil
}

// askDiscardLines asks before discarding lines from the work tree.
func (a *App) askDiscardLines(t *tab, chosen func(int) bool) {
	d := &t.cv.diff
	p := d.res.Parsed
	n := 0
	for i, l := range p.Lines {
		if (l.Kind == '+' || l.Kind == '-') && chosen(i) {
			n++
		}
	}
	op, err := a.linesOp(t, chosen, false, true)
	if err != nil {
		a.notice = errText(err)
		return
	}
	path := d.key.path
	w := t.s.Where()
	head := t.s.State.Head
	update := a.Update
	go func() {
		files, err := write.Observe(w.WorkTree, []string{path})
		update(func() {
			if err != nil {
				a.notice = errText(err)
				return
			}
			op.Expect = &write.Expect{Head: hex.EncodeToString(head.OID), Files: files}
			if !head.Detached {
				op.Expect.Branch = head.Target
			}
			shown, _ := showPath(path)
			lines := fmt.Sprintf("%d changed lines", n)
			if n == 1 {
				lines = "1 changed line"
			}
			t.cv.confirm = confirmState{
				open:    true,
				title:   "Discard " + lines + "?",
				message: "This takes " + lines + " in " + shown + " back to what is staged.\n\nA snapshot is kept: Undo or the Snapshots view brings them back.",
				button:  "Discard",
				run: func() {
					if _, err := t.s.Submit(op); err != nil {
						a.notice = errText(err)
					}
					d.picked.Clear()
				},
			}
		})
	}()
}
