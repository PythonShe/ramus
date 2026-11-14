package ui

import (
	"encoding/hex"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/redact"
	"ramus/internal/git/refs"
	"ramus/internal/git/repo"
	"ramus/internal/git/write"
	"ramus/internal/session"
)

// commitOps adds what acts on commits (hex, newest first) to a menu:
// cherry-pick, revert, save as a patch, bisect.
func (a *App) commitOps(m *ui.Menu, t *tab, ids func() []string, n int) {
	ro := t.s.Queue == nil
	head := t.s.State.Head
	onto := "HEAD"
	if !head.Detached {
		onto = strings.TrimPrefix(head.Target, "refs/heads/")
	}
	what := "Commit"
	if n > 1 {
		what = fmt.Sprintf("%d Commits", n)
	}
	m.Separator()
	if m.Item("Cherry-Pick " + what + " onto " + redact.Control(onto)).Disabled(ro || len(head.OID) == 0).Chosen() {
		list := ids()
		slices.Reverse(list)
		op, err := write.CherryPick(t.s.Where(), list, t.s.Seen())
		a.submit(t, op, err)
	}
	if m.Item("Revert " + what).Disabled(ro || len(head.OID) == 0).Chosen() {
		op, err := write.Revert(t.s.Where(), ids(), t.s.Seen())
		a.submit(t, op, err)
	}
	if m.Item("Save " + what + " as Patch…").Disabled(a.SaveFile == nil).Chosen() {
		list := ids()
		slices.Reverse(list)
		name := list[0][:10] + ".patch"
		if len(list) > 1 {
			name = fmt.Sprintf("%d-commits.patch", len(list))
		}
		a.savePatch(t, session.PatchSave{Commits: list}, name)
	}
	if n == 2 && m.Item("Compare These Commits").Chosen() {
		list := ids() // newest first
		from, _ := hex.DecodeString(list[1])
		to, _ := hex.DecodeString(list[0])
		a.compareCommits(t, from, to, list[1][:10], list[0][:10])
	}
	if n == 1 && len(head.OID) > 0 && m.Item("Compare with HEAD").Chosen() {
		from, _ := hex.DecodeString(ids()[0])
		a.compareCommits(t, from, head.OID, shortID(from), "HEAD")
	}
	bs := t.bisect.state
	if n == 2 && !bs.Active {
		if m.Item("Bisect Between These").Disabled(ro).Chosen() {
			list := ids()
			op, err := write.BisectStart(t.s.Where(), list[0], list[1:], t.s.Seen())
			a.submit(t, op, err)
		}
	}
	if n == 1 {
		m.Submenu("Bisect", func(sub *ui.Menu) {
			for _, v := range []struct {
				label string
				v     write.BisectVerdict
			}{{"Mark Bad", write.BisectBad}, {"Mark Good", write.BisectGood}, {"Skip", write.BisectSkip}} {
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
	if hv.picked.Len() < 2 || !hv.picked.Has(k) {
		id := hex.EncodeToString(h.OID(k))
		return func() []string { return []string{id} }, 1
	}
	n := hv.picked.Len()
	return func() []string {
		// Rows are newest first; walk them until every pick is found.
		var out []string
		for r := 0; r < snap.Len() && len(out) < n; r++ {
			if c := snap.Commit(r); hv.picked.Has(c) {
				out = append(out, hex.EncodeToString(h.OID(c)))
			}
		}
		return out
	}, n
}

// savePatch asks where to save a patch, then writes it there.
func (a *App) savePatch(t *tab, req session.PatchSave, name string) {
	if a.SaveFile == nil {
		return
	}
	save, update := a.SaveFile, a.Update
	go func() {
		file, err := save("Save Patch", name)
		update(func() {
			switch {
			case err != nil:
				a.notice = errText(err)
			case file != "":
				t.s.SavePatch(req, file, func(err error) {
					if err != nil {
						a.notice = errText(err)
					}
				})
			}
		})
	}()
}

// applyPatchFile asks for a patch file, reads what it changes, and asks
// before applying it.
func (a *App) applyPatchFile(t *tab) {
	if a.PickFile == nil || t.s.Queue == nil {
		return
	}
	pick, update := a.PickFile, a.Update
	go func() {
		file, err := pick("Apply Patch")
		update(func() {
			switch {
			case err != nil:
				a.notice = errText(err)
			case file != "":
				t.s.ReadPatchFile(file, func(pf session.PatchFile, err error) {
					if err != nil {
						a.notice = errText(err)
						return
					}
					a.askApply(t, pf)
				})
			}
		})
	}()
}

func (a *App) askApply(t *tab, pf session.PatchFile) {
	base := filepath.Base(pf.Path)
	files := strings.Join(pf.Files[:min(len(pf.Files), 5)], ", ")
	if len(pf.Files) > 5 {
		files += fmt.Sprintf(" and %d more", len(pf.Files)-5)
	}
	title, msg := "Apply "+base+"?", "It changes "+plural(len(pf.Files), "file")+": "+redact.Control(files)+"."
	if pf.Mailbox {
		title = "Apply " + base + " as commits?"
		msg += " Each patch in it becomes a commit on the current branch."
	}
	op, err := write.ApplyPatch(t.s.Where(), pf.Path, pf.Mailbox, pf.Files, t.s.Seen())
	if err != nil {
		a.notice = errText(err)
		return
	}
	a.confirm = confirmState{open: true, title: title, message: msg, button: "Apply", run: func() { a.submit(t, op, nil) }}
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
			a.notice = errText(err)
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
	ui.Row(c).Padding(6, 8).Gap(8).AlignItems(ui.Center).Background(th.Surface).Children(func() {
		var what string
		switch {
		case bs.FirstBad != "":
			what = "First bad commit: " + bs.FirstBad[:10]
		case bs.Left >= 0:
			what = "Bisecting: " + strconv.Itoa(bs.Left) + " left to test, about " + plural(bs.Steps, "step")
		case bs.Bad == "":
			what = "Bisecting: mark a bad commit"
		default:
			what = "Bisecting: mark a good commit"
		}
		ui.Text(c, what).Bold().SingleLine().Grow(1)
		if bs.FirstBad != "" {
			if ui.Button(c, "Show").Clicked() {
				id, _ := hex.DecodeString(bs.FirstBad)
				a.chooseCommit(t, id)
			}
		} else {
			for _, v := range []struct {
				label string
				v     write.BisectVerdict
			}{{"Good", write.BisectGood}, {"Bad", write.BisectBad}, {"Skip", write.BisectSkip}} {
				if ui.Button(c, v.label).Disabled(ro).Clicked() {
					op, err := write.BisectMark(w, v.v, "", false)
					a.submit(t, op, err)
				}
			}
		}
		if ui.Button(c, "End Bisect").Disabled(ro).Clicked() {
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
	ui.Modal(c, &cs.open, func() {
		ui.Column(c).Width(560).Height(440).Gap(12).Padding(20).Children(func() {
			ui.Text(c, "Clean Untracked Files").Bold()
			if ui.Checkbox(c, &cs.ignored, "Include ignored files").Changed() {
				a.listClean()
			}
			switch {
			case cs.loading:
				ui.Column(c).Grow(1).Center().Children(func() { ui.Spinner(c) })
			case cs.err != "":
				ui.Text(c, cs.err).TextColor(th.Danger).Selectable().Grow(1)
			case len(cs.paths) == 0:
				ui.Column(c).Grow(1).Center().Children(func() {
					ui.Text(c, "Nothing to clean").TextColor(th.TextMuted)
				})
			default:
				paths := cs.paths
				cs.list.Label = func(i int) string { return paths[i] }
				ui.List(c, &cs.list, len(paths), func(i int) {
					shown, why := showPath(paths[i])
					txt := ui.Text(c, shown).Font("monospace").FontSize(12).SingleLine().Padding(2, 4)
					if why != "" {
						txt.TextColor(th.Warning).Description(why)
					}
				}).Label("Files to delete").Grow(1)
			}
			n := len(cs.paths)
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				label := "Delete"
				if n > 0 {
					label = "Delete " + plural(n, "Item")
				}
				// The destructive choice is never the default one.
				if ui.Button(c, label).Disabled(n == 0 || cs.loading || t.s.Queue == nil).Clicked() {
					op, err := write.Clean(t.s.Where(), cs.paths, cs.ignored, cs.seen)
					a.submit(t, op, err)
					cs.open = false
				}
				if ui.PrimaryButton(c, "Cancel").Clicked() {
					cs.open = false
				}
			})
		})
	})
}
