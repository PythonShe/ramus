package ui

import (
	"encoding/hex"
	"strings"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/write"
)

// branchDialog asks for the name of a branch to make at a commit.
type branchDialog struct {
	open     bool
	t        *tab
	commit   string
	name     string
	checkout bool
	err      string
}

// commitActions adds what can be done from a commit to a menu: check it
// out, branch from it, reset the current branch to it.
func (a *App) commitActions(m *ui.Menu, t *tab, id []byte) {
	ro := t.s.Queue == nil
	commit := hex.EncodeToString(id)
	if m.Item("Check Out Commit").Disabled(ro).Chosen() {
		a.submit(t, write.Detach(t.s.Where(), commit, t.s.Seen()), nil)
	}
	if m.Item("Create Branch Here…").Disabled(ro).Chosen() {
		a.branchUI = branchDialog{open: true, t: t, commit: commit, checkout: true}
	}
	head := t.s.State.Head
	target := "HEAD"
	if !head.Detached {
		target = strings.TrimPrefix(head.Target, "refs/heads/")
	}
	m.Submenu("Reset "+target+" to Here", func(sub *ui.Menu) {
		for _, mode := range []struct {
			label string
			mode  write.ResetMode
		}{
			{"Soft: Keep Index and Files", write.ResetSoft},
			{"Mixed: Keep Files", write.ResetMixed},
			{"Keep: Update Files, Keep Local Changes", write.ResetKeep},
			{"Hard: Drop Local Changes…", write.ResetHard},
		} {
			if sub.Item(mode.label).Disabled(ro || len(head.OID) == 0).Chosen() {
				a.reset(t, commit, mode.mode)
			}
		}
	})
}

// submit runs a write and shows why it could not start.
func (a *App) submit(t *tab, op write.Op, err error) {
	if err == nil {
		_, err = t.s.Submit(op)
	}
	if err != nil {
		a.notice = errText(err)
	}
}

func (a *App) reset(t *tab, commit string, mode write.ResetMode) {
	op, err := write.Reset(t.s.Where(), commit, mode, t.s.Seen())
	if err != nil || mode != write.ResetHard {
		a.submit(t, op, err)
		return
	}
	a.confirm = confirmState{
		open:    true,
		title:   "Drop local changes and reset to " + commit[:10] + "?",
		message: "Changed files are kept in a snapshot first, and the branch in a backup; Undo brings both back.",
		button:  "Reset",
		run:     func() { a.submit(t, op, nil) },
	}
}

func (a *App) branchView(c *ui.Context) {
	d := &a.branchUI
	if !d.open || d.t == nil {
		return
	}
	th := c.Theme()
	ui.Modal(c, &d.open, func() {
		ui.Column(c).Width(420).Gap(12).Padding(20).Children(func() {
			ui.Text(c, "Create Branch at "+d.commit[:10]).Bold()
			in := ui.TextInput(c, &d.name).Placeholder("Branch name").Label("Branch name").AutoFocus()
			ui.Checkbox(c, &d.checkout, "Switch to it")
			if d.err != "" {
				ui.Text(c, d.err).FontSize(12).TextColor(th.Danger)
			}
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				if ui.Button(c, "Cancel").Clicked() {
					d.open = false
				}
				if ui.PrimaryButton(c, "Create").Disabled(strings.TrimSpace(d.name) == "").Clicked() || in.Submitted() {
					t := d.t
					op, err := write.CreateBranch(t.s.Where(), strings.TrimSpace(d.name), d.commit, d.checkout, t.s.Seen())
					if err == nil {
						_, err = t.s.Submit(op)
					}
					if err != nil {
						d.err = errText(err)
						return
					}
					d.open = false
				}
			})
		})
	})
}
