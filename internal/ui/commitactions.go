package ui

import (
	"encoding/hex"
	"strings"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/write"
	"ramus/locales"
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
	if m.Item(locales.CommitCheckOut()).Disabled(ro).Chosen() {
		a.submit(t, write.Detach(t.s.Where(), commit, t.s.Seen()), nil)
	}
	if m.Item(locales.CommitCreateBranchHere()).Disabled(ro).Chosen() {
		a.branchUI = branchDialog{open: true, t: t, commit: commit, checkout: true}
	}
	if m.Item(locales.CommitCreateTagHere()).Disabled(ro).Chosen() {
		a.tagDialog(t, commit)
	}
	head := t.s.State.Head
	target := "HEAD"
	if !head.Detached {
		target = strings.TrimPrefix(head.Target, "refs/heads/")
	}
	m.Submenu(locales.CommitResetTo(target), func(sub *ui.Menu) {
		for _, mode := range []struct {
			label string
			mode  write.ResetMode
		}{
			{locales.CommitResetSoft(), write.ResetSoft},
			{locales.CommitResetMixed(), write.ResetMixed},
			{locales.CommitResetKeep(), write.ResetKeep},
			{locales.CommitResetHard(), write.ResetHard},
		} {
			if sub.Item(mode.label).Disabled(ro || len(head.OID) == 0).Chosen() {
				a.reset(t, commit, mode.mode)
			}
		}
	})
	a.rewriteActions(m, t, id)
	a.commitOps(m, t, func() []string { return []string{commit} }, 1)
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
		a.submitRewrite(t, op, err)
		return
	}
	a.confirm = confirmState{
		open:    true,
		title:   locales.CommitResetHardTitle(commit[:10]),
		message: locales.CommitResetHardMessage(),
		button:  locales.CommitReset(),
		run:     func() { a.submitRewrite(t, op, nil) },
	}
}

func (a *App) branchView(c *ui.Context) {
	d := &a.branchUI
	if !d.open || d.t == nil {
		return
	}
	th := c.Theme()
	ui.Modal(c, &d.open, func() {
		dialogBox(c).Width(dialogNarrow).Children(func() {
			title := locales.CommitCreateBranchAt(d.commit[:10])
			if h := d.t.s.State.Head; !h.Detached && h.Target != "" && hex.EncodeToString(h.OID) == d.commit {
				title = locales.CommitCreateBranchFrom(strings.TrimPrefix(h.Target, "refs/heads/"))
			}
			ui.Text(c, title).Bold()
			in := ui.TextInput(c, &d.name).Placeholder(locales.CommonBranchName()).Label(locales.CommonBranchName()).AutoFocus()
			ui.Checkbox(c, &d.checkout, locales.CommitSwitchToBranch())
			if d.err != "" {
				ui.Text(c, d.err).FontSize(textSmall).TextColor(th.Danger)
			}
			ui.Row(c).Gap(spaceM).Children(func() {
				ui.Spacer(c)
				if ui.Button(c, locales.CommonCancel()).Clicked() {
					d.open = false
				}
				empty := strings.TrimSpace(d.name) == ""
				if (ui.PrimaryButton(c, locales.CommonCreate()).Disabled(empty).Clicked() || in.Submitted()) && !empty {
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
