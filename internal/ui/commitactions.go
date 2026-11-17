package ui

import (
	"encoding/hex"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/redact"
	"ramus/internal/git/write"
	"ramus/locales"
)

// branchDialog asks for the name of a branch to make at a commit.
type branchDialog struct {
	open   bool
	t      *tab
	commit string
	// from is the branch the dialog was opened from, named in its
	// title, "" for one opened on a commit.
	from     string
	name     string
	checkout bool
	err      string
}

// commitItems adds what can be done from commit id to a menu, in groups:
// check it out, branch or tag it; bring it onto HEAD or HEAD to it; the
// Edit Commit submenu, which rewrites it; compare, save and bisect.
// merges is the number of its parents past the first, which a merge is
// cherry-picked or reverted against.
func (a *App) commitItems(m *ui.Menu, t *tab, id []byte, merges int) {
	ro := t.s.Queue == nil
	commit := hex.EncodeToString(id)
	ids := func() []string { return []string{commit} }
	if m.Item(locales.CommitCheckOut()).Disabled(ro).Chosen() {
		a.submit(t, write.Detach(t.s.Where(), commit, t.s.Seen()), nil)
	}
	if m.Item(locales.CommitCreateBranchHere()).Disabled(ro).Chosen() {
		a.branchUI = branchDialog{open: true, t: t, commit: commit, checkout: true}
	}
	if m.Item(locales.CommitCreateTagHere()).Disabled(ro).Chosen() {
		a.tagDialog(t, commit)
	}
	m.Separator()
	a.pickOps(m, t, ids, 1, merges)
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
				a.reset(t, commit, mode.mode, target)
			}
		}
	})
	m.Separator()
	// What rewrites the commit stands apart, Drop with it, so that no
	// slip of the pointer from a common item reaches it.
	m.Submenu(locales.RewriteEditCommit(), func(sub *ui.Menu) { a.rewriteActions(sub, t, id) })
	m.Separator()
	a.commitOps(m, t, ids, 1)
}

// submit runs a write and shows why it could not start.
func (a *App) submit(t *tab, op write.Op, err error) {
	var j *write.Job
	if err == nil {
		j, err = t.s.Submit(op)
	}
	if err != nil {
		a.notice = errText(err)
		return
	}
	a.watchSync(t, op, j)
}

// submitUndoable runs a write that moves HEAD, as a cherry-pick, a revert
// or a reset; once it went well, a toast says so, done, with Undo, which
// undoes it while it is still the last write. rewrote is set for a write
// that may have dropped commits the branch's upstream has.
func (a *App) submitUndoable(t *tab, op write.Op, err error, done string, rewrote bool) {
	if err != nil {
		a.notice = errText(err)
		return
	}
	seen := t.s.Seen()
	j, err := t.s.Submit(op)
	if err != nil {
		a.notice = errText(err)
		return
	}
	q := t.s.Queue
	t.s.Then(j, func(err error) {
		if err != nil {
			return
		}
		if rewrote {
			a.checkPushed(t, seen)
		}
		toast := ui.Toast{ID: "undo", Title: done}
		if id := j.Entry(); id != 0 {
			toast.Action = locales.CommonUndo()
			toast.OnAction = func() { a.undoWrite(t, q, id) }
		}
		a.tabToast(t, toast)
	})
}

// undoWrite takes back the write of tab t's queue q whose job recorded
// History entry id, for a toast's Undo: only while it is the write Undo
// would take back, in a tab still open; else a toast says nothing was
// undone, and the write is left to the Snapshots view. It reports
// whether the undo was queued.
func (a *App) undoWrite(t *tab, q *write.Queue, id uint64) bool {
	if slices.Contains(a.tabs, t) && t.s.Queue == q {
		_, ok, err := t.s.UndoWrite(id)
		if err != nil {
			a.notice = errText(err)
			return false
		}
		if ok {
			return true
		}
	}
	a.tabToast(t, ui.Toast{ID: "undo", Title: locales.CommonUndoTooLate()})
	return false
}

// reset resets target, the branch checked out or HEAD, to commit.
func (a *App) reset(t *tab, commit string, mode write.ResetMode, target string) {
	op, err := write.Reset(t.s.Where(), commit, mode, t.s.Seen())
	done := locales.CommitResetDone(redact.Control(target), commit[:10])
	if err != nil || mode != write.ResetHard {
		a.submitUndoable(t, op, err, done, true)
		return
	}
	a.confirm = confirmState{
		open:    true,
		title:   locales.CommitResetHardTitle(commit[:10]),
		message: locales.CommitResetHardMessage(),
		button:  locales.CommitReset(),
		run:     func() { a.submitUndoable(t, op, nil, done, true) },
	}
}

func (a *App) branchView(c *ui.Context) {
	d := &a.branchUI
	if !d.open || d.t == nil {
		return
	}
	th := c.Theme()
	a.modal(c, &d.open, func() {
		dialogBox(c).Width(dialogNarrow).Children(func() {
			title := locales.CommitCreateBranchAt(d.commit[:10])
			if h := d.t.s.State.Head; d.from == "" && !h.Detached && h.Target != "" && hex.EncodeToString(h.OID) == d.commit {
				d.from = h.Target
			}
			if d.from != "" {
				title = locales.CommitCreateBranchFrom(redact.Control(strings.TrimPrefix(d.from, "refs/heads/")))
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
