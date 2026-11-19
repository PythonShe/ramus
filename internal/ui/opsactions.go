package ui

import (
	"encoding/hex"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/lanes"
	"ramus/internal/git/redact"
	"ramus/internal/git/refs"
	"ramus/internal/git/repo"
	"ramus/internal/git/write"
	"ramus/internal/session"
	"ramus/locales"
)

// pickOps adds what applies commits (hex, newest first) to HEAD to a
// menu: cherry-pick and revert. One merge, of merges parents past the
// first, has a submenu for each, of the parents to take its change
// against: git takes a merge with no parent named for neither. Of
// several, merges counts the merges among them, which turn both off:
// git would stop at the first merge, the commits before it applied.
func (a *App) pickOps(m *ui.Menu, t *tab, ids func() []string, n, merges int) {
	onto, off := pickOnto(t)
	pick := func(mainline int) {
		list := ids()
		if mainline == 0 {
			a.cherryPick(t, list)
			return
		}
		op, err := write.CherryPickMerge(t.s.Where(), list[0], mainline, t.s.Seen())
		a.submitUndoable(t, op, err, locales.OpsCherryPicked(len(list), onto), false)
	}
	revert := func(mainline int) {
		list := ids()
		op, err := write.Revert(t.s.Where(), list, t.s.Seen())
		if mainline > 0 {
			op, err = write.RevertMerge(t.s.Where(), list[0], mainline, t.s.Seen())
		}
		a.submitUndoable(t, op, err, locales.OpsReverted(len(list)), false)
	}
	if n == 1 && merges > 0 {
		m.Submenu(locales.OpsCherryPick(n, onto), func(sub *ui.Menu) {
			for p := 1; p <= merges+1; p++ {
				if sub.Item(locales.OpsCherryPickAgainst(p)).Disabled(off).Chosen() {
					pick(p)
				}
			}
		}).Disabled(off)
		m.Submenu(locales.OpsRevert(n), func(sub *ui.Menu) {
			for p := 1; p <= merges+1; p++ {
				if sub.Item(locales.OpsRevertKeeping(p)).Disabled(off).Chosen() {
					revert(p)
				}
			}
		}).Disabled(off)
		return
	}
	why := ""
	if merges > 0 {
		why = locales.OpsPickedMerge()
	}
	if m.Item(locales.OpsCherryPick(n, onto)).Disabled(off || why != "").Tooltip(why).Chosen() {
		pick(0)
	}
	if m.Item(locales.OpsRevert(n)).Disabled(off || why != "").Tooltip(why).Chosen() {
		revert(0)
	}
}

// pickOnto names what commits are cherry-picked onto, the branch checked
// out or HEAD, and reports whether nothing can be: a repository only
// read, or with no commit yet.
func pickOnto(t *tab) (onto string, off bool) {
	head := t.s.State.Head
	onto = "HEAD"
	if !head.Detached {
		onto = strings.TrimPrefix(head.Target, "refs/heads/")
	}
	return redact.Control(onto), t.s.Queue == nil || len(head.OID) == 0
}

// cherryPick applies commits (hex, newest first, none a merge) onto
// HEAD, oldest first.
func (a *App) cherryPick(t *tab, ids []string) {
	onto, _ := pickOnto(t)
	list := slices.Clone(ids)
	slices.Reverse(list)
	op, err := write.CherryPick(t.s.Where(), list, t.s.Seen())
	a.submitUndoable(t, op, err, locales.OpsCherryPicked(len(list), onto), false)
}

// commitOps adds what else acts on commits (hex, newest first) to a
// menu: save as a patch, compare, bisect.
func (a *App) commitOps(m *ui.Menu, t *tab, ids func() []string, n int) {
	ro := t.s.Queue == nil
	head := t.s.State.Head
	if m.Item(locales.OpsSaveAsPatch(n)).Disabled(a.SaveFile == nil).Chosen() {
		list := ids()
		slices.Reverse(list)
		name := list[0][:10] + ".patch"
		if len(list) > 1 {
			name = strconv.Itoa(len(list)) + "-commits.patch"
		}
		a.savePatch(t, session.PatchSave{Commits: list}, name)
	}
	if n == 2 && m.Item(locales.OpsCompareTheseCommits()).Chosen() {
		list := ids() // newest first
		from, _ := hex.DecodeString(list[1])
		to, _ := hex.DecodeString(list[0])
		a.compareCommits(t, from, to, list[1][:10], list[0][:10])
	}
	if n == 1 && len(head.OID) > 0 && m.Item(locales.OpsCompareWithHead()).Chosen() {
		from, _ := hex.DecodeString(ids()[0])
		a.compareCommits(t, from, head.OID, shortID(from), "HEAD")
	}
	if n == 1 && m.Item(locales.OpsCompareWithWorkTree()).Disabled(t.s.Where().WorkTree == "").Chosen() {
		from, _ := hex.DecodeString(ids()[0])
		a.compareWorkTree(t, from, shortID(from))
	}
	bs := t.bisect.state
	if n == 2 && !bs.Active {
		if m.Item(locales.OpsBisectBetween()).Disabled(ro).Chosen() {
			list := ids()
			op, err := write.BisectStart(t.s.Where(), list[0], list[1:], t.s.Seen())
			a.submit(t, op, err)
		}
	}
	if n == 1 {
		m.Submenu(locales.OpsBisect(), func(sub *ui.Menu) {
			for _, v := range []struct {
				label string
				v     write.BisectVerdict
			}{{locales.OpsMarkBad(), write.BisectBad}, {locales.OpsMarkGood(), write.BisectGood}, {locales.OpsBisectSkip(), write.BisectSkip}} {
				if sub.Item(v.label).Disabled(ro || v.v == write.BisectSkip && !bs.Active).Chosen() {
					op, err := write.BisectMark(t.s.Where(), v.v, ids()[0], !bs.Active)
					a.submit(t, op, err)
				}
			}
		})
	}
}

// historyIDs are the commits a history menu acts on, newest first: those
// picked when the row is one of them, else the row's.
func (a *App) historyIDs(t *tab, h session.HistoryState, i int) (func() []string, int) {
	hv := &t.hist
	snap := h.Snap
	k := snap.Commit(i)
	if hv.picked.Len() < 2 || !hv.picked.Has(string(h.OID(k))) {
		id := hex.EncodeToString(h.OID(k))
		return func() []string { return []string{id} }, 1
	}
	// Counted as the actions take them: the commits the history shows,
	// though more may be picked while it is read again.
	return func() []string { return pickedIDs(t, h) }, len(pickedIDs(t, h))
}

// pickedIDs are the commits picked in the history (hex, newest first).
func pickedIDs(t *tab, h session.HistoryState) []string {
	hv := &t.hist
	snap := h.Snap
	n := hv.picked.Len()
	// Rows are newest first; walk them until every pick is found.
	var out []string
	for r := 0; snap != nil && r < snap.Len() && len(out) < n; r++ {
		if id := h.OID(snap.Commit(r)); hv.picked.Has(string(id)) {
			out = append(out, hex.EncodeToString(id))
		}
	}
	return out
}

// pickedBar shows, under the history, how many commits are picked while
// several are, with what acts on them all: compare two, cherry-pick
// them, or let them go.
func (a *App) pickedBar(c *ui.Context, t *tab, h session.HistoryState) {
	hv := &t.hist
	if hv.picked.Len() < 2 || h.Snap == nil {
		return
	}
	// The picks the history shows and the merges among them, counted
	// again as the picks or the history change, not on every frame: the
	// walk may go far down.
	if k := (pickedKey{h.Snap, hv.picked.Len(), string(hv.chosenID)}); k != hv.mergesFor {
		hv.mergesFor = k
		hv.pickedN, hv.merges = pickedCount(t, h)
	}
	n := hv.pickedN
	if n < 2 {
		return
	}
	th := c.Theme()
	_, off := pickOnto(t)
	why := ""
	if hv.merges > 0 {
		why = locales.OpsPickedMerge()
	}
	ui.Row(c).Padding(barPadY, insetX).Gap(spaceM).AlignItems(ui.Center).Background(th.Surface).Children(func() {
		ui.Text(c, locales.HistoryPicked(n)).Bold().SingleLine().Grow(1)
		if n == 2 && ui.Button(c, locales.HistoryPickedCompare()).Clicked() {
			if ids := pickedIDs(t, h); len(ids) == 2 {
				from, _ := hex.DecodeString(ids[1])
				to, _ := hex.DecodeString(ids[0])
				a.compareCommits(t, from, to, ids[1][:10], ids[0][:10])
			}
		}
		if ui.Button(c, locales.HistoryPickedCherryPick()).Disabled(off || why != "").Tooltip(why).Clicked() {
			// Counted afresh: a merge must never go in with the others.
			if _, merges := pickedCount(t, h); merges == 0 {
				a.cherryPick(t, pickedIDs(t, h))
			}
		}
		if ui.Button(c, locales.HistoryPickedClear()).Clicked() {
			hv.unpick()
		}
	})
}

// pickedKey is what the merges among the commits picked were counted
// for.
type pickedKey struct {
	snap   *lanes.Snapshot
	n      int
	chosen string
}

// pickedCount counts the commits picked that the history shows, and the
// merges among them, walking its rows until every pick is found, as
// historyIDs does: once, as the menu opens.
func pickedCount(t *tab, h session.HistoryState) (found, merges int) {
	hv := &t.hist
	snap := h.Snap
	n := hv.picked.Len()
	for r := 0; snap != nil && r < snap.Len() && found < n; r++ {
		if hv.picked.Has(string(h.OID(snap.Commit(r)))) {
			found++
			if snap.Merges(r) > 0 {
				merges++
			}
		}
	}
	return found, merges
}

// prunePicked lets go of the commits picked that the history, read to
// its end, no longer has: a filter hid them, or no branch shown reaches
// them now. What is picked is then what the actions take, and the walks
// counting the picks stop at the oldest.
func (hv *historyState) prunePicked(h session.HistoryState) {
	n := hv.picked.Len()
	if n == 0 || !h.Done || h.Snap == nil {
		return
	}
	snap := h.Snap
	found := make(map[string]bool, n)
	for r := 0; r < snap.Len() && len(found) < n; r++ {
		if id := string(h.OID(snap.Commit(r))); hv.picked.Has(id) {
			found[id] = true
		}
	}
	if len(found) == n {
		return
	}
	var gone []string
	for id := range hv.picked.All() {
		if !found[id] {
			gone = append(gone, id)
		}
	}
	for _, id := range gone {
		hv.picked.Remove(id)
	}
}

// savePatch asks where to save a patch, then writes it there.
func (a *App) savePatch(t *tab, req session.PatchSave, name string) {
	if a.SaveFile == nil {
		return
	}
	save, update := a.SaveFile, a.Update
	a.goWork(t, func() {
		file, err := save(locales.OpsSavePatchTitle(), name)
		update(func() {
			switch {
			case err != nil:
				a.tell(t, errText(err))
			case file != "":
				t.s.SavePatch(req, file, func(err error) {
					if err != nil {
						a.tell(t, errText(err))
					}
				})
			}
		})
	})
}

// applyPatchFile asks for a patch file, reads what it changes, and asks
// before applying it.
func (a *App) applyPatchFile(t *tab) {
	if a.PickFile == nil || t.s.Queue == nil {
		return
	}
	pick, update := a.PickFile, a.Update
	a.goWork(t, func() {
		file, err := pick(locales.OpsApplyPatchTitle())
		update(func() {
			switch {
			case err != nil:
				a.tell(t, errText(err))
			case file != "":
				t.s.ReadPatchFile(file, func(pf session.PatchFile, err error) {
					if err != nil {
						a.tell(t, errText(err))
						return
					}
					a.askApply(t, pf)
				})
			}
		})
	})
}

func (a *App) askApply(t *tab, pf session.PatchFile) {
	base := filepath.Base(pf.Path)
	files := strings.Join(pf.Files[:min(len(pf.Files), 5)], ", ")
	if len(pf.Files) > 5 {
		files = locales.OpsApplyMoreFiles(len(pf.Files)-5, files)
	}
	files = redact.Control(files)
	title, msg := locales.OpsApplyTitle(base), locales.OpsApplyMessage(len(pf.Files), files)
	if pf.Mailbox {
		title, msg = locales.OpsApplyMailboxTitle(base), locales.OpsApplyMailboxMessage(len(pf.Files), files)
	}
	op, err := write.ApplyPatch(t.s.Where(), pf.Path, pf.Mailbox, pf.Files, t.s.Seen())
	if err != nil {
		a.notice = errText(err)
		return
	}
	a.confirm = confirmState{open: true, title: title, message: msg, button: locales.OpsApply(), safe: true, run: func() { a.submit(t, op, nil) }}
}

// bisectState is a tab's bisect in progress, read again as refs change.
type bisectState struct {
	state   session.BisectState
	readFor *refs.List
	readW   int
	readOp  repo.Operation
	loading bool
}

func (a *App) loadBisect(t *tab) {
	bs := &t.bisect
	st := t.s.State
	if bs.loading || (st.Refs == bs.readFor && t.s.Writes == bs.readW && st.Operation == bs.readOp) {
		return
	}
	bs.readFor, bs.readW, bs.readOp, bs.loading = st.Refs, t.s.Writes, st.Operation, true
	t.s.LoadBisect(func(s session.BisectState, err error) {
		bs.loading, bs.state = false, s
		if err != nil {
			a.tell(t, errText(err))
		}
	})
}

// bisectBar shows a bisect in progress over the history: how far it is,
// and the marks for the commit checked out.
func (a *App) bisectBar(c *ui.Context, t *tab) {
	a.loadBisect(t)
	bs := t.bisect.state
	if !bs.Active {
		return
	}
	th := c.Theme()
	ro := t.s.Queue == nil
	w := t.s.Where()
	ui.Row(c).Padding(barPadY, insetX).Gap(spaceM).AlignItems(ui.Center).Background(th.Surface).Children(func() {
		var what string
		switch {
		case bs.FirstBad != "":
			what = locales.OpsBisectFirstBad(bs.FirstBad[:10])
		case bs.Left >= 0:
			what = locales.OpsBisectProgress(bs.Left, bs.Steps)
		case bs.Bad == "":
			what = locales.OpsBisectMarkBad()
		default:
			what = locales.OpsBisectMarkGood()
		}
		ui.Text(c, what).Bold().SingleLine().Grow(1)
		if bs.FirstBad != "" {
			if ui.Button(c, locales.OpsBisectShow()).Clicked() {
				id, _ := hex.DecodeString(bs.FirstBad)
				a.chooseCommit(t, id)
			}
		} else {
			for _, v := range []struct {
				label string
				v     write.BisectVerdict
			}{{locales.OpsBisectGood(), write.BisectGood}, {locales.OpsBisectBad(), write.BisectBad}, {locales.OpsBisectSkip(), write.BisectSkip}} {
				if ui.Button(c, v.label).Disabled(ro).Clicked() {
					op, err := write.BisectMark(w, v.v, "", false)
					a.submit(t, op, err)
				}
			}
		}
		if ui.Button(c, locales.OpsEndBisect()).Disabled(ro).Clicked() {
			a.submit(t, write.BisectReset(w), nil)
		}
	})
}

// cleanState lists what git clean would delete, to delete it.
type cleanState struct {
	open    bool
	t       *tab
	ignored bool
	loading bool
	paths   []string
	seen    []write.FileState
	err     string
	list    ui.ListState
}

func (a *App) openClean(t *tab) {
	a.cleanUI = cleanState{open: true, t: t}
	a.listClean()
}

func (a *App) listClean() {
	cs := &a.cleanUI
	cs.loading, cs.paths, cs.err = true, nil, ""
	ignored, t := cs.ignored, cs.t
	t.s.ListClean(ignored, func(paths []string, seen []write.FileState, err error) {
		if cs.t != t || cs.ignored != ignored {
			return
		}
		cs.loading, cs.paths, cs.seen = false, paths, seen
		if err != nil {
			cs.err = errText(err)
		}
	})
}

func (a *App) cleanView(c *ui.Context) {
	cs := &a.cleanUI
	if !cs.open || cs.t == nil {
		return
	}
	th := c.Theme()
	t := cs.t
	a.modal(c, &cs.open, func() {
		dialogBox(c).Width(dialogMedium).Height(440).Children(func() {
			ui.Text(c, locales.OpsCleanTitle()).Bold()
			if ui.Checkbox(c, &cs.ignored, locales.OpsIncludeIgnoredFiles()).Changed() {
				a.listClean()
			}
			switch {
			case cs.loading:
				loading(c, "")
			case cs.err != "":
				ui.Text(c, cs.err).TextColor(th.Danger).Selectable().Grow(1)
			case len(cs.paths) == 0:
				ui.Column(c).Grow(1).Center().Children(func() {
					ui.Text(c, locales.OpsNothingToClean()).TextColor(th.TextMuted)
				})
			default:
				paths := cs.paths
				cs.list.Label = func(i int) string { return paths[i] }
				ui.List(c, &cs.list, len(paths), func(i int) {
					shown, why := showPath(paths[i])
					txt := pathLabel(c, shown).Font("monospace").FontSize(textSmall).Padding(rowPadY, insetX)
					if why != "" {
						txt.TextColor(th.Warning).Description(why)
					}
				}).Label(locales.OpsFilesToDelete()).Grow(1)
			}
			n := len(cs.paths)
			ui.Row(c).Gap(spaceM).Children(func() {
				ui.Spacer(c)
				label := locales.CommonDelete()
				if n > 0 {
					label = locales.OpsDeleteItems(n)
				}
				// The destructive choice is never the default one.
				if ui.Button(c, label).Disabled(n == 0 || cs.loading || t.s.Queue == nil).Clicked() {
					op, err := write.Clean(t.s.Where(), cs.paths, cs.ignored, cs.seen)
					a.submit(t, op, err)
					cs.open = false
				}
				if ui.PrimaryButton(c, locales.CommonCancel()).Clicked() {
					cs.open = false
				}
			})
		})
	})
}
