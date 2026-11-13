package ui

import (
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/redact"
	"ramus/internal/git/refs"
	"ramus/internal/git/write"
	"ramus/internal/session"
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
	err       string
}

func (a *App) openStash(t *tab) {
	a.stashUI = stashDialog{open: true, t: t, untracked: true}
}

func (a *App) stashDialogView(c *ui.Context) {
	d := &a.stashUI
	if !d.open || d.t == nil {
		return
	}
	th := c.Theme()
	ui.Modal(c, &d.open, func() {
		ui.Column(c).Width(420).Gap(12).Padding(20).Children(func() {
			ui.Text(c, "Stash Changes").Bold()
			in := ui.TextInput(c, &d.message).Placeholder("Message").Label("Message").AutoFocus()
			ui.Checkbox(c, &d.untracked, "Include untracked files")
			ui.Checkbox(c, &d.keepIndex, "Keep staged changes in place")
			if d.err != "" {
				ui.Text(c, d.err).FontSize(12).TextColor(th.Danger)
			}
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				if ui.Button(c, "Cancel").Clicked() {
					d.open = false
				}
				if ui.PrimaryButton(c, "Stash").Disabled(d.t.s.Queue == nil).Clicked() || in.Submitted() {
					op := write.Stash(d.t.s.Where(), write.StashOptions{Message: d.message, Untracked: d.untracked, KeepIndex: d.keepIndex})
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
			ui.Row(c).Padding(6, 8).Gap(8).AlignItems(ui.Center).Children(func() {
				if d := ss.dropped; d != nil {
					ui.Text(c, "Dropped "+hex.EncodeToString(d.OID)[:10]).FontSize(12).TextColor(th.TextMuted).SingleLine().Grow(1)
					if ui.Button(c, "Restore").Disabled(t.s.Queue == nil).Clicked() {
						op, err := write.StashRestore(t.s.Where(), hex.EncodeToString(d.OID), d.Message)
						a.submit(t, op, err)
						ss.dropped = nil
					}
				} else {
					ui.Spacer(c)
				}
				if ui.Button(c, "Stash Changes…").Disabled(t.s.Queue == nil).Clicked() {
					a.openStash(t)
				}
			})
			switch {
			case ss.err != "":
				ui.Text(c, ss.err).TextColor(th.Danger).Padding(12).Selectable()
				return
			case len(ss.entries) == 0:
				ui.Column(c).Fill().Center().Children(func() {
					if ss.loading {
						ui.Spinner(c)
						return
					}
					ui.Text(c, "No stashes").TextColor(th.TextMuted)
				})
				return
			}
			entries := ss.entries
			ss.list.Selected = &ss.chosen
			ss.list.Label = func(i int) string { return "stash@{" + strconv.Itoa(i) + "}, " + redact.Control(entries[i].Message) }
			now := a.Now()
			var act func()
			ui.List(c, &ss.list, len(entries), func(i int) {
				e := entries[i]
				row := ui.Row(c).Padding(3, 12).Gap(8).AlignItems(ui.Center).FillWidth()
				row.Children(func() {
					ui.Text(c, "stash@{"+strconv.Itoa(i)+"}").Font("monospace").FontSize(12).Width(80).TextColor(th.TextMuted).SingleLine()
					ui.Text(c, redact.Control(e.Message)).SingleLine().Grow(1)
					ui.Text(c, ago(now, e.Time)).FontSize(12).Width(88).TextColor(th.TextMuted).SingleLine()
				})
				row.ContextMenu(func(m *ui.Menu) { a.stashMenu(m, t, i, e, &act) })
			}).Label("Stash entries").Grow(1)
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

func (a *App) stashMenu(m *ui.Menu, t *tab, n int, e session.ReflogEntry, act *func()) {
	ss := &t.stash
	ro := t.s.Queue == nil
	entry := write.StashEntry{N: n, Commit: hex.EncodeToString(e.OID)}
	w := t.s.Where()
	if m.Item("Apply").Disabled(ro).Chosen() {
		*act = func() {
			op, err := write.StashApply(w, entry, false)
			a.submit(t, op, err)
		}
	}
	if m.Item("Apply with Staged Changes").Disabled(ro).Chosen() {
		*act = func() {
			op, err := write.StashApply(w, entry, true)
			a.submit(t, op, err)
		}
	}
	if m.Item("Pop").Disabled(ro).Chosen() {
		*act = func() {
			op, err := write.StashPop(w, entry, false)
			a.submit(t, op, err)
		}
	}
	if m.Item("Create Branch from Stash…").Disabled(ro).Chosen() {
		*act = func() {
			seen := t.s.Seen()
			ss.branch = refDialog{open: true, title: "Branch from stash@{" + strconv.Itoa(n) + "}", labels: []string{"Branch name"}, values: []string{""}, button: "Create",
				run: func(d *refDialog) (write.Op, error) {
					return write.StashBranch(w, entry, strings.TrimSpace(d.values[0]), seen)
				}}
		}
	}
	m.Separator()
	if m.Item("Drop…").Disabled(ro).Danger().Chosen() {
		*act = func() {
			ss.ask = confirmState{open: true, title: "Drop stash@{" + strconv.Itoa(n) + "} (" + entry.Commit[:10] + ")?",
				message: redact.Control(e.Message) + ". Restore puts it back while git keeps its commit.", button: "Drop",
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
	if m.Item("Copy Hash").Chosen() {
		a.clip = entry.Commit
	}
}
