package ui

import (
	"encoding/hex"
	"strings"

	"github.com/egoist/mygo/ui"

	"ramus/internal/cli"
	"ramus/internal/git/diff"
	"ramus/internal/git/pathsafe"
	"ramus/internal/git/redact"
	"ramus/internal/git/status"
	"ramus/internal/git/write"
	"ramus/internal/session"
	"ramus/internal/tools"
	"ramus/locales"
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
	// resKey is what res was read for: res stays shown while the patch
	// of key is read, and lines are applied only to the file they are of.
	resKey diffKey
	pv     patchView
	pager  patchPager
	// load reads the patch again for the pager's next step.
	load func()
}

func newDiffState() diffState { return diffState{} }

// chosenFile is the file row chosen in the changes list, if any: an
// entry's, or a file's in an untracked folder.
func (cv *changesState) chosenFile() (changeRow, bool) {
	m := cv.model
	if m == nil || cv.chosen < 0 || cv.chosen >= len(m.rows) || !fileRow(m.rows[cv.chosen]) {
		return changeRow{}, false
	}
	return m.rows[cv.chosen], true
}

func (a *App) detail(c *ui.Context, t *tab) {
	cv := &t.cv
	r, ok := cv.chosenFile()
	if !ok {
		t.merge.stopLoad()
		if m := cv.model; m != nil && len(m.rows) == 0 && m.filter == "" {
			a.cleanTree(c, t)
			return
		}
		emptyPane(c)
		return
	}
	m := cv.model
	i := int(r.idx)
	if r.sec == secConflicts {
		a.mergeEditor(c, t, string(m.st.Path(i)))
		return
	}
	// The conflicted file left for another: its read stops.
	t.merge.stopLoad()
	p := m.path(r)
	if strings.HasSuffix(p, "/") {
		// git diffs no folder: a patch of it would say there is no
		// difference. Its files are listed under it instead.
		a.folderPane(c, t, m, r)
		return
	}
	key := diffKey{path: p, src: diff.Unstaged, st: m.st, ws: diff.Whitespace(a.diffOpts().ws)}
	sides := session.Sides{WorkTreePath: p}
	if r.kind == rowFile {
		switch {
		case r.sec == secStaged:
			key.src = diff.Staged
		case m.st.Kind(i) == status.Untracked:
			key.src = diff.Untracked
		}
		key.orig = renamedFrom(m.st, i, key.src)
		sides = patchSides(m.st, i, key.src)
	} else {
		key.src = diff.Untracked
	}
	d := &cv.diff
	a.readPatch(t, key, sides)
	ui.Column(c).Fill().Children(func() {
		a.diffHeader(c, t, m, r)
		a.pagerNotice(c, &d.pager, d.res, d.loading, locales.PatchShownInPartNoStaging(sizeText(d.pager.max())), d.load)
		a.whitespaceNotice(c, d)
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

// readPatch reads the patch of key, whose whole files sides names, into
// the changes view's, unless that is the one it holds.
func (a *App) readPatch(t *tab, key diffKey, sides session.Sides) {
	d := &t.cv.diff
	if d.key == key {
		return
	}
	if d.key.path != key.path || d.key.src != key.src {
		d.pager = patchPager{}
	}
	if d.resKey.path != key.path || d.resKey.src != key.src {
		// The lines picked are of the file shown, not of the one chosen.
		d.pv.clearSelection()
	}
	d.key = key
	var load func()
	load = func() {
		d.loading = true
		req := diff.PatchRequest{Source: key.src, Paths: key.paths(), Context: -1, MaxBytes: d.pager.max(), Whitespace: key.ws, Renames: key.orig != ""}
		sides.Max = d.pager.sides()
		t.s.Patch(req, sides, func(res session.PatchResult) {
			if d.key != key {
				return
			}
			if reopened(res.Err) {
				// Read again as shown next, the patch shown staying.
				d.key, d.loading = diffKey{}, false
				return
			}
			d.res, d.resKey = res, key
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
	if !d.partialOK() {
		return
	}
	ro := t.s.Queue == nil
	lines := d.pv.changeLines(change)
	if r.sec == secStaged {
		if ui.Button(c, locales.DiffUnstage()).Disabled(ro).Clicked() {
			a.applyLines(t, lines, true, true)
		}
		return
	}
	if ui.Button(c, locales.DiffDiscardChange()).Disabled(ro).Clicked() {
		a.askDiscardLines(t, lines)
	}
	if ui.Button(c, locales.DiffStage()).Disabled(ro).Clicked() {
		a.applyLines(t, lines, true, false)
	}
}

// partialOK reports whether lines of this patch can be staged, unstaged
// or discarded one by one: not while the patch shown is another file's
// or section's, kept until the one chosen is read. A newer status of the
// same file may differ; git apply's context refuses a stale patch.
func (d *diffState) partialOK() bool {
	k, rk := d.key, d.resKey
	return k.path == rk.path && k.orig == rk.orig && k.src == rk.src && k.ws == rk.ws && partialOK(d.res, rk)
}

func partialOK(res session.PatchResult, key diffKey) bool {
	p := res.Parsed
	// Lines of a rename applied back would undo the rename with them. A
	// file in LFS shows its content, but its patch is of the pointers,
	// which a part of would break.
	return p != nil && len(p.Files) == 1 && !p.Files[0].Binary && !p.Files[0].Quoted &&
		!res.Truncated && !p.Cut && !res.LFS && key.src != diff.Untracked && key.ws == diff.AllWhitespace &&
		key.orig == ""
}

// linesOK reports whether lines chosen of a patch partialOK allows can be
// taken in the direction reverse says: not those of a new file to take
// back, nor of a deleted one to take, which git applies only whole.
func linesOK(res session.PatchResult, reverse bool) bool {
	return res.Parsed.Partial(0, reverse) == nil
}

func (a *App) diffHeader(c *ui.Context, t *tab, m *changeModel, r changeRow) {
	th := c.Theme()
	d := &t.cv.diff
	ro := t.s.Queue == nil
	ui.Row(c).Padding(barPadY, insetX).MinHeight(barHeight).Gap(spaceM).AlignItems(ui.Center).Children(func() {
		shown, _ := showPath(m.path(r))
		if d.key.orig != "" {
			// A rename names where the file came from, which a patch of no
			// differences in content would not show.
			from, _ := showPath(d.key.orig)
			shown = locales.DiffRenamedPath(from, shown)
		}
		pathLabel(c, shown).Bold().Grow(1)
		ui.Text(c, t.cv.rowWords(m, r)).FontSize(textSmall).TextColor(th.TextMuted)
		fileLines(c, t.cv.rowLines(m, r))
		picked, n := d.pv.selectedLines()
		if !d.partialOK() || n == 0 {
			// The file's own actions, which lines picked take the place of.
			if r.sec != secStaged && ui.Button(c, locales.DiffDiscardChange()).Label(locales.ChangesDiscardFiles(1)).Disabled(ro).Clicked() {
				a.discardRows(t, m, r.sec, []changeRow{r})
			}
			label := locales.ChangesStageFiles(1)
			if r.sec == secStaged {
				label = locales.ChangesUnstageFiles(1)
			}
			if ui.Button(c, label).Disabled(ro).Clicked() {
				a.toggleStage(t, m, []changeRow{r})
			}
		}
		if d.partialOK() && n > 0 {
			if r.sec == secStaged {
				if linesOK(d.res, true) && ui.Button(c, locales.DiffUnstageLines(n)).Disabled(ro).Clicked() {
					a.applyLines(t, picked, true, true)
				}
			} else {
				if linesOK(d.res, true) && ui.Button(c, locales.DiffDiscardLines(n)).Disabled(ro).Clicked() {
					a.askDiscardLines(t, picked)
				}
				if linesOK(d.res, false) && ui.Button(c, locales.DiffStageLines(n)).Disabled(ro).Clicked() {
					a.applyLines(t, picked, true, false)
				}
			}
		}
	})
	a.patchBar(c, false, a.showsText(d.res, &d.pv), func() {
		if ui.Button(c, locales.ActionsOpenInEditor()).Disabled(!a.canOpenInEditor(t)).Clicked() {
			a.openInEditor(t, d.key.path)
		}
		if ui.Button(c, locales.DiffOpenInTool()).Disabled(ro || d.key.src == diff.Untracked).Clicked() {
			old, new := locales.CompareSideIndex(), locales.CompareSideWorkTree()
			if d.key.src == diff.Staged {
				old, new = locales.CompareSideHead(), locales.CompareSideIndex()
			}
			t.s.OpenDiffTool(session.DiffToolRequest{Paths: d.key.paths(), Cached: d.key.src == diff.Staged, Own: a.ownDiffTool(old, new)}, a.toolDone)
		}
	})
}

// whitespaceNotice says why lines cannot be staged while white space is
// ignored, which the viewer otherwise drops without a word, and offers to
// show it all.
func (a *App) whitespaceNotice(c *ui.Context, d *diffState) {
	all := d.key
	all.ws = diff.AllWhitespace
	if d.key.ws == diff.AllWhitespace || !partialOK(d.res, all) {
		return
	}
	ui.Row(c).Padding(rowPadY, insetX).Gap(spaceM).AlignItems(ui.Center).Children(func() {
		ui.Text(c, locales.DiffWhitespaceNoStaging()).FontSize(textSmall).TextColor(c.Theme().TextMuted).Grow(1)
		if ui.Button(c, locales.DiffAllWhiteSpace()).Clicked() {
			a.diffOpts().ws = int(diff.AllWhitespace)
		}
	})
}

// binaryPatch reports whether res is a binary file's patch, which the
// options of text do nothing to.
func binaryPatch(res session.PatchResult) bool {
	p := res.Parsed
	return p != nil && len(p.Files) > 0 && p.Files[0].Binary
}

// showsText reports whether a patch shows lines, which the options of
// text change: not a binary file's, nor a picture's shown as one.
func (a *App) showsText(res session.PatchResult, pv *patchView) bool {
	return !binaryPatch(res) && !(pv.pics.text && a.diffOpts().svg == 1)
}

// canOpenInEditor reports whether openInEditor can open a file of t: an
// editor is chosen and the repository, whose work tree it is in, open.
func (a *App) canOpenInEditor(t *tab) bool {
	_, ok := a.editor(t)
	return ok && t.s.Repo != nil
}

// openInEditor opens the file at rel, of the work tree, in the editor
// chosen; the caller offers it as canOpenInEditor says.
func (a *App) openInEditor(t *tab, rel string) {
	ed, ok := a.editor(t)
	r := t.s.Repo
	if !ok || r == nil {
		return
	}
	root, update := r.WorkTree, a.Update
	a.goWork(t, func() {
		abs, err := pathsafe.Target(root, rel)
		if err == nil {
			err = ed.Open(abs, 0)
		}
		if err != nil {
			update(func() { a.tell(t, errText(err)) })
		}
	})
}

// cleanTree fills the patch's pane when nothing is changed with what
// there is to do next: send or take the commits the branch is ahead or
// behind by, or open the repository.
func (a *App) cleanTree(c *ui.Context, t *tab) {
	th := c.Theme()
	ro := t.s.Queue == nil
	head := t.s.State.Head
	// As the sync button read it, for the branch checked out.
	var tr session.Track
	if t.sync.trackOf == head.Target {
		tr = t.sync.track
	}
	w := t.s.Where()
	ui.Column(c).Fill().Center().Gap(spaceL).Children(func() {
		ui.Text(c, locales.ChangesNoLocal()).TextColor(th.TextMuted)
		ui.Row(c).Gap(spaceM).Children(func() {
			if tr.Upstream != "" && !tr.Gone && !head.Detached {
				upstream := redact.Control(strings.TrimPrefix(tr.Upstream, "refs/remotes/"))
				if tr.Behind > 0 && nameButton(c, locales.SyncPullLabel(tr.Behind, upstream)).Disabled(ro || t.s.Fetching()).Clicked() {
					a.syncPull(t, "")
				}
				if pushTo, ahead := pushTarget(tr); ahead > 0 && nameButton(c, locales.SyncPushLabel(ahead, pushTo)).Disabled(ro || t.s.Fetching()).Clicked() {
					a.submit(t, write.Push(w, write.PushOptions{Branch: head.Target}), nil)
				}
			}
			dir, update := t.s.Path, a.Update
			if ed, ok := a.editor(t); ok && ui.Button(c, locales.ActionsOpenInEditor()).Clicked() {
				a.goWork(t, func() {
					if err := ed.Open(dir, 0); err != nil {
						update(func() { a.tell(t, errText(err)) })
					}
				})
			}
			if ui.Button(c, locales.ActionsReveal()).Clicked() {
				a.goOpen(t, tools.Reveal, dir)
			}
		})
	})
}

// ownDiffTool is the command git runs for the app's own compare window
// as its difftool, for a repository whose config names no diff tool,
// with the sides named old and new; "" when the app does not know its
// own path.
func (a *App) ownDiffTool(old, new string) string {
	if a.Program == "" {
		return ""
	}
	return cli.LabeledDiffCommand(a.Program, old, new)
}

// toolDone shows why an external tool did not run.
func (a *App) toolDone(err error) {
	if err != nil {
		a.notice = errText(err)
	}
}

// lineTarget is a file's patch that lines are staged, unstaged or
// discarded from, and the viewer that selects them.
type lineTarget struct {
	p    *diff.Parsed
	path string
	pv   *patchView
}

// shownLines is the target of the file shown in the changes view.
func (t *tab) shownLines() lineTarget {
	d := &t.cv.diff
	return lineTarget{p: d.res.Parsed, path: d.resKey.path, pv: &d.pv}
}

// applyLines stages (cached, not reverse), unstages (cached, reverse) or
// discards (reverse) the chosen lines of the file shown.
func (a *App) applyLines(t *tab, chosen func(int) bool, cached, reverse bool) {
	a.applyLinesOf(t, t.shownLines(), chosen, cached, reverse)
}

func (a *App) applyLinesOf(t *tab, lt lineTarget, chosen func(int) bool, cached, reverse bool) {
	op, err := linesOp(t, lt, chosen, cached, reverse)
	if err == nil {
		_, err = t.s.Submit(op)
	}
	if err != nil {
		a.notice = errText(err)
	}
	lt.pv.clearSelection()
}

func linesOp(t *tab, lt lineTarget, chosen func(int) bool, cached, reverse bool) (write.Op, error) {
	patch, err := lt.p.Select(0, reverse, chosen)
	if err != nil {
		return write.Op{}, err
	}
	return write.Apply(t.s.Where(), write.Partial{Path: lt.path, Patch: patch, Cached: cached, Reverse: reverse}), nil
}

// askDiscardLines asks before discarding lines of the file shown from
// the work tree.
func (a *App) askDiscardLines(t *tab, chosen func(int) bool) {
	a.askDiscardLinesOf(t, t.shownLines(), &t.cv.confirm, chosen)
}

func (a *App) askDiscardLinesOf(t *tab, lt lineTarget, confirm *confirmState, chosen func(int) bool) {
	n := 0
	for i, l := range lt.p.Lines {
		if (l.Kind == '+' || l.Kind == '-') && chosen(i) {
			n++
		}
	}
	op, err := linesOp(t, lt, chosen, false, true)
	if err != nil {
		a.notice = errText(err)
		return
	}
	path := lt.path
	w := t.s.Where()
	head := t.s.State.Head
	update := a.Update
	a.goWork(t, func() {
		files, err := write.Observe(w.WorkTree, []string{path})
		update(func() {
			if err != nil {
				a.tell(t, errText(err))
				return
			}
			op.Expect = &write.Expect{Head: hex.EncodeToString(head.OID), Files: files}
			if !head.Detached {
				op.Expect.Branch = head.Target
			}
			shown, _ := showPath(path)
			*confirm = confirmState{
				open:    true,
				title:   locales.DiffDiscardLinesTitle(n),
				message: locales.DiffDiscardLinesMessage(n, shown),
				button:  locales.ChangesDiscard(),
				run: func() {
					if _, err := t.s.Submit(op); err != nil {
						a.notice = errText(err)
					}
					lt.pv.clearSelection()
				},
			}
		})
	})
}
