package ui

import (
	"encoding/hex"
	"fmt"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/diff"
	"ramus/internal/git/status"
	"ramus/internal/git/write"
	"ramus/internal/session"
)

// diffKey is what a shown patch was read for; a new status reads it again.
type diffKey struct {
	path string
	// orig is the path a renamed file had, "" when it was not renamed in
	// the section shown.
	orig string
	src  diff.Source
	st   *status.Status
	ws   diff.Whitespace
}

// diffState is the patch of the file chosen in the changes list.
type diffState struct {
	key     diffKey
	loading bool
	res     session.PatchResult
	pv      patchView
	pager   patchPager
	// load reads the patch again for the pager's next step.
	load func()
}

func newDiffState() diffState { return diffState{} }

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
	if r.sec == secConflicts {
		a.mergeEditor(c, t, string(m.st.Path(i)))
		return
	}
	key := diffKey{path: string(m.st.Path(i)), src: diff.Unstaged, st: m.st, ws: diff.Whitespace(a.diffOpts().ws)}
	switch {
	case r.sec == secStaged:
		key.src = diff.Staged
	case m.st.Kind(i) == status.Untracked:
		key.src = diff.Untracked
	}
	key.orig = renamedFrom(m.st, i, key.src)
	d := &cv.diff
	if d.key != key {
		if d.key.path != key.path || d.key.src != key.src {
			d.pager = patchPager{}
		}
		d.key = key
		sides := patchSides(m.st, i, key.src)
		var load func()
		load = func() {
			d.loading = true
			req := diff.PatchRequest{Source: key.src, Paths: key.paths(), Context: -1, MaxBytes: d.pager.max(), Whitespace: key.ws, Renames: key.orig != ""}
			sides.Max = d.pager.sides()
			t.s.Patch(req, sides, func(res session.PatchResult) {
				if d.key != key {
					return
				}
				d.res = res
				a.setPatch(&d.pv, res)
				if d.pager.more(res) {
					load()
					return
				}
				d.loading = false
			})
		}
		d.load = load
		load()
	}
	ui.Column(c).Fill().Children(func() {
		a.diffHeader(c, t, m, r)
		a.pagerNotice(c, &d.pager, d.res, d.loading, "; lines cannot be staged from it", d.load)
		res := d.res
		if d.loading && res.Parsed == nil {
			res = session.PatchResult{}
		}
		var actions func(c *ui.Context, change int)
		if d.partialOK() {
			actions = func(c *ui.Context, change int) { a.changeActions(c, t, r, change) }
		}
		a.patchBody(c, t, &d.pv, res, actions)
	})
}

// renamedFrom is the old path of entry i when status found it renamed in
// the part src shows: staged, or in the work tree for a file added with
// intent to add.
func renamedFrom(st *status.Status, i int, src diff.Source) string {
	if st.Kind(i) != status.Renamed {
		return ""
	}
	x, y := st.XY(i)
	if (src == diff.Staged && x == 'R') || (src == diff.Unstaged && y == 'R') {
		return string(st.OrigPath(i))
	}
	return ""
}

// paths are what the patch is read for: the old path first for a rename.
func (k diffKey) paths() []string {
	if k.orig != "" {
		return []string{k.orig, k.path}
	}
	return []string{k.path}
}

// patchSides names the whole files of a work-tree change: what the index
// or HEAD holds, and the work tree's file or the index's.
func patchSides(st *status.Status, i int, src diff.Source) session.Sides {
	path := string(st.Path(i))
	modes := st.Modes(i)
	switch src {
	case diff.Untracked:
		return session.Sides{WorkTreePath: path}
	case diff.Staged:
		if ids := st.OIDs(i); len(ids) == 2 {
			return session.Sides{Old: ids[0], New: ids[1], Modes: [2]uint32{modes[0], modes[1]}}
		}
	default:
		if ids := st.OIDs(i); len(ids) == 2 {
			return session.Sides{Old: ids[1], WorkTreePath: path, Modes: [2]uint32{modes[1], modes[2]}}
		}
	}
	return session.Sides{}
}

// changeActions are the buttons on a change of the patch shown.
func (a *App) changeActions(c *ui.Context, t *tab, r changeRow, change int) {
	d := &t.cv.diff
	ro := t.s.Queue == nil
	lines := d.pv.changeLines(change)
	if r.sec == secStaged {
		if ui.Button(c, "Unstage").Disabled(ro).Clicked() {
			a.applyLines(t, lines, true, true)
		}
		return
	}
	if ui.Button(c, "Discard…").Disabled(ro).Clicked() {
		a.askDiscardLines(t, lines)
	}
	if ui.Button(c, "Stage").Disabled(ro).Clicked() {
		a.applyLines(t, lines, true, false)
	}
}

// partialOK reports whether lines of this patch can be staged, unstaged
// or discarded one by one.
func (d *diffState) partialOK() bool {
	p := d.res.Parsed
	// Lines of a rename applied back would undo the rename with them.
	return p != nil && len(p.Files) == 1 && !p.Files[0].Binary && !p.Files[0].Quoted &&
		!d.res.Truncated && !p.Cut && d.key.src != diff.Untracked && d.key.ws == diff.AllWhitespace &&
		d.key.orig == ""
}

func (a *App) diffHeader(c *ui.Context, t *tab, m *changeModel, r changeRow) {
	th := c.Theme()
	d := &t.cv.diff
	ro := t.s.Queue == nil
	ui.Row(c).Padding(6, 12).Gap(8).AlignItems(ui.Center).Children(func() {
		shown, _ := showPath(string(m.st.Path(int(r.idx))))
		ui.Text(c, shown).Bold().SingleLine().Grow(1)
		ui.Text(c, entryWords(m.st, int(r.idx), r.sec)).FontSize(12).TextColor(th.TextMuted)
		if picked, n := d.pv.selectedLines(); d.partialOK() && n > 0 {
			lines := fmt.Sprintf(" %d Lines", n)
			if n == 1 {
				lines = " 1 Line"
			}
			if r.sec == secStaged {
				if ui.Button(c, "Unstage"+lines).Disabled(ro).Clicked() {
					a.applyLines(t, picked, true, true)
				}
			} else {
				if ui.Button(c, "Discard"+lines+"…").Disabled(ro).Clicked() {
					a.askDiscardLines(t, picked)
				}
				if ui.Button(c, "Stage"+lines).Disabled(ro).Clicked() {
					a.applyLines(t, picked, true, false)
				}
			}
		}
	})
	a.diffBar(c, func() {
		if ui.Button(c, "Open in Diff Tool").Disabled(ro || d.key.src == diff.Untracked).Clicked() {
			t.s.OpenDiffTool(session.DiffToolRequest{Paths: d.key.paths(), Cached: d.key.src == diff.Staged}, a.toolDone)
		}
	})
}

// toolDone shows why an external tool did not run.
func (a *App) toolDone(err error) {
	if err != nil {
		a.notice = errText(err)
	}
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
	t.cv.diff.pv.clearSelection()
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
					d.pv.clearSelection()
				},
			}
		})
	}()
}
