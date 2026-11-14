package ui

import (
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/gitexe"
	"ramus/internal/git/redact"
	"ramus/internal/git/refs"
	"ramus/internal/git/write"
	"ramus/internal/session"
	"ramus/locales"
)

// stashState is a tab's list of stashes and the one chosen.
type stashState struct {
	readFor *refs.List
	readW   int
	loading bool
	entries []session.ReflogEntry
	err     string
	list    ui.ListState
	chosen  int
	detail  commitView
	detailW float32
	ask     confirmState
	// dropped is the last stash dropped here, until it is restored or
	// another is dropped.
	dropped *session.ReflogEntry
	branch  refDialog
}

func newStashState() stashState {
	return stashState{chosen: -1, detailW: 560, detail: commitView{split: 360}}
}

// stashDialog asks how to stash local changes.
type stashDialog struct {
	open      bool
	t         *tab
	message   string
	untracked bool
	keepIndex bool
	// paths, when set, are the files stashed; else what is 0 for every
	// change and 1 for what is staged alone.
	paths []string
	what  int
	err   string
}

func (a *App) openStash(t *tab) {
	a.stashUI = stashDialog{open: true, t: t, untracked: true}
}

// openStashPaths asks how to stash the changes to paths alone.
func (a *App) openStashPaths(t *tab, paths []string) {
	a.stashUI = stashDialog{open: true, t: t, untracked: true, paths: paths}
}

// canStashStaged reports whether git can stash what is staged alone.
func canStashStaged(t *tab) bool {
	return t.s.Repo != nil && t.s.Repo.Runner.Git().Version.Has(gitexe.StashStaged)
}

func (a *App) stashDialogView(c *ui.Context) {
	d := &a.stashUI
	if !d.open || d.t == nil {
		return
	}
	th := c.Theme()
	ui.Modal(c, &d.open, func() {
		dialogBox(c).Width(dialogNarrow).Children(func() {
			title := locales.StashTitle()
			if n := len(d.paths); n > 0 {
				title = locales.StashFilesTitle(n)
				if n == 1 {
					title = locales.StashFileTitle(d.paths[0])
				}
			}
			ui.Text(c, title).Bold()
			in := ui.TextInput(c, &d.message).Placeholder(locales.CommonMessage()).Label(locales.CommonMessage()).AutoFocus()
			if len(d.paths) == 0 && canStashStaged(d.t) {
				ui.Segmented(c, &d.what, locales.StashAllChanges(), locales.StashStagedOnly())
			}
			staged := len(d.paths) == 0 && d.what == 1
			if !staged {
				ui.Checkbox(c, &d.untracked, locales.StashIncludeUntracked())
				ui.Checkbox(c, &d.keepIndex, locales.StashKeepIndex())
			}
			if d.err != "" {
				ui.Text(c, d.err).FontSize(textSmall).TextColor(th.Danger)
			}
			ui.Row(c).Gap(spaceM).Children(func() {
				ui.Spacer(c)
				if ui.Button(c, locales.CommonCancel()).Clicked() {
					d.open = false
				}
				if ui.PrimaryButton(c, locales.StashStash()).Disabled(d.t.s.Queue == nil).Clicked() || in.Submitted() {
					op := write.Stash(d.t.s.Where(), write.StashOptions{Message: d.message, Untracked: d.untracked, KeepIndex: d.keepIndex,
						Paths: d.paths, Staged: staged})
					if _, err := d.t.s.Submit(op); err != nil {
						d.err = errText(err)
						return
					}
					d.open = false
				}
			})
		})
	})
}

func (a *App) stashView(c *ui.Context, t *tab) {
	th := c.Theme()
	ss := &t.stash
	if refs := t.s.State.Refs; (refs != ss.readFor || t.s.Writes != ss.readW) && !ss.loading {
		ss.readFor, ss.readW, ss.loading = refs, t.s.Writes, true
		t.s.Reflog("refs/stash", func(es []session.ReflogEntry, err error) {
			ss.loading, ss.entries, ss.err = false, es, ""
			if st := t.s.State.Status; err != nil && st != nil && st.Branch.Stashes > 0 {
				ss.err = errText(err)
			}
			if ss.chosen >= len(es) {
				ss.chosen = len(es) - 1
			}
		})
	}
	ui.Split(c, &ss.detailW, func() {
		ui.Column(c).Fill().Children(func() {
			ui.Row(c).Padding(barPadY, insetX).Gap(spaceM).AlignItems(ui.Center).Children(func() {
				if d := ss.dropped; d != nil {
					ui.Text(c, locales.StashDropped(hex.EncodeToString(d.OID)[:10])).FontSize(textSmall).TextColor(th.TextMuted).SingleLine().Grow(1)
					if ui.Button(c, locales.StashRestore()).Disabled(t.s.Queue == nil).Clicked() {
						op, err := write.StashRestore(t.s.Where(), hex.EncodeToString(d.OID), d.Message)
						a.submit(t, op, err)
						ss.dropped = nil
					}
				} else {
					ui.Spacer(c)
				}
				if ui.Button(c, locales.StashChangesEllipsis()).Disabled(t.s.Queue == nil).Clicked() {
					a.openStash(t)
				}
			})
			switch {
			case ss.err != "":
				errorMessage(c, ss.err)
				return
			case len(ss.entries) == 0:
				ui.Column(c).Fill().Center().Children(func() {
					if ss.loading {
						ui.Spinner(c)
						return
					}
					ui.Text(c, locales.StashNone()).TextColor(th.TextMuted)
				})
				return
			}
			entries := ss.entries
			ss.list.Selected = &ss.chosen
			ss.list.Label = func(i int) string { return stashName(i) + ", " + redact.Control(entries[i].Message) }
			now := a.Now()
			var act func()
			ui.List(c, &ss.list, len(entries), func(i int) {
				e := entries[i]
				row := ui.Row(c).Padding(rowPadY, insetX).Gap(spaceM).AlignItems(ui.Center).FillWidth()
				row.Children(func() {
					ui.Text(c, stashName(i)).Font("monospace").FontSize(textSmall).Width(80).TextColor(th.TextMuted).SingleLine()
					ui.Text(c, redact.Control(e.Message)).SingleLine().Grow(1)
					ui.Text(c, ago(now, e.Time)).FontSize(textSmall).Width(88).TextColor(th.TextMuted).SingleLine()
				})
				row.ContextMenu(func(m *ui.Menu) { a.stashMenu(m, t, i, e, &act) })
			}).Label(locales.StashEntriesLabel()).Grow(1)
			if act != nil {
				act()
			}
		})
	}, func() {
		var id []byte
		if ss.chosen >= 0 && ss.chosen < len(ss.entries) {
			id = ss.entries[ss.chosen].OID
		}
		a.commitDetail(c, t, id, &ss.detail)
	}).SplitReset(560).Fill()
	a.confirmView(c, &ss.ask)
	a.refDialogView(c, &ss.branch, t)
}

// stashName is git's name of the stash entry n.
func stashName(n int) string { return "stash@{" + strconv.Itoa(n) + "}" }

func (a *App) stashMenu(m *ui.Menu, t *tab, n int, e session.ReflogEntry, act *func()) {
	ss := &t.stash
	ro := t.s.Queue == nil
	entry := write.StashEntry{N: n, Commit: hex.EncodeToString(e.OID)}
	w := t.s.Where()
	if m.Item(locales.StashApply()).Disabled(ro).Chosen() {
		*act = func() {
			op, err := write.StashApply(w, entry, false)
			a.submit(t, op, err)
		}
	}
	if m.Item(locales.StashApplyStaged()).Disabled(ro).Chosen() {
		*act = func() {
			op, err := write.StashApply(w, entry, true)
			a.submit(t, op, err)
		}
	}
	if m.Item(locales.StashPop()).Disabled(ro).Chosen() {
		*act = func() {
			op, err := write.StashPop(w, entry, false)
			a.submit(t, op, err)
		}
	}
	if m.Item(locales.StashCreateBranch()).Disabled(ro).Chosen() {
		*act = func() {
			seen := t.s.Seen()
			ss.branch = refDialog{open: true, title: locales.StashBranchTitle(stashName(n)), labels: []string{locales.CommonBranchName()}, values: []string{""}, button: locales.CommonCreate(),
				run: func(d *refDialog) (write.Op, error) {
					return write.StashBranch(w, entry, strings.TrimSpace(d.values[0]), seen)
				}}
		}
	}
	m.Separator()
	if m.Item(locales.StashDropEllipsis()).Disabled(ro).Danger().Chosen() {
		*act = func() {
			ss.ask = confirmState{open: true, title: locales.StashDropTitle(stashName(n), entry.Commit[:10]),
				message: locales.StashDropMessage(redact.Control(e.Message)), button: locales.StashDrop(),
				run: func() {
					op, err := write.StashDrop(w, entry)
					if err == nil {
						_, err = t.s.Submit(op)
					}
					if err != nil {
						a.notice = errText(err)
						return
					}
					ss.dropped = &e
				}}
		}
	}
	if m.Item(locales.CommonCopyHash()).Chosen() {
		a.clip = entry.Commit
	}
}
