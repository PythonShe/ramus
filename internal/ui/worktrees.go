package ui

import (
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/redact"
	"ramus/internal/git/repo"
	"ramus/internal/git/write"
	"ramus/locales"
)

// worktreeDialog makes a worktree of a branch: a local one checked out
// there, or a remote one with a new local branch tracking it.
type worktreeDialog struct {
	open bool
	t    *tab
	// branch is the full name of the branch the worktree is for.
	branch string
	remote bool
	// fresh makes the worktree of a new branch started at HEAD, named by
	// name, and follows the branch's name with the folder's until the
	// folder is edited; autoPath is the folder as last suggested.
	fresh    bool
	autoPath string
	// name is the new branch's, for a remote branch.
	name, path string
	openTab    bool
	err        string
}

// worktreeRemoval is the dialog removing a worktree.
type worktreeRemoval struct {
	open bool
	t    *tab
	wt   repo.Worktree
	// withBranch also deletes the branch checked out there; force removes
	// it with changes or untracked files.
	withBranch, force bool
}

// worktreeLock asks why a worktree is locked.
type worktreeLock struct {
	open   bool
	t      *tab
	path   string
	reason string
}

// openAddWorktree starts the dialog for branch, a full ref name.
func (a *App) openAddWorktree(t *tab, branch string) {
	d := worktreeDialog{open: true, t: t, branch: branch, openTab: true}
	short := strings.TrimPrefix(branch, "refs/heads/")
	if rest, ok := strings.CutPrefix(branch, "refs/remotes/"); ok {
		d.remote = true
		// origin/topic makes topic.
		_, short, _ = strings.Cut(rest, "/")
		d.name = short
	}
	d.path = worktreePath(t, short)
	a.worktreeUI = d
}

// openNewWorktree starts the dialog for a worktree of a new branch.
func (a *App) openNewWorktree(t *tab) {
	d := worktreeDialog{open: true, t: t, fresh: true, openTab: true}
	d.path = worktreePath(t, "")
	d.autoPath = d.path
	a.worktreeUI = d
}

// worktreePath is the folder suggested for a worktree of branch: beside
// the main one, named after both.
func worktreePath(t *tab, branch string) string {
	main := t.s.Path
	if wts := t.s.State.Worktrees; len(wts) > 0 && !wts[0].Bare {
		main = wts[0].Path
	}
	name := strings.NewReplacer("/", "-", `\`, "-").Replace(branch)
	if name == "" {
		return filepath.Join(filepath.Dir(main), filepath.Base(main)+"-")
	}
	return filepath.Join(filepath.Dir(main), filepath.Base(main)+"-"+name)
}

func (a *App) worktreeView(c *ui.Context) {
	d := &a.worktreeUI
	if !d.open || d.t == nil {
		return
	}
	th := c.Theme()
	short := strings.TrimPrefix(strings.TrimPrefix(d.branch, "refs/heads/"), "refs/remotes/")
	if d.fresh && d.path == d.autoPath {
		d.autoPath = worktreePath(d.t, strings.TrimSpace(d.name))
		d.path = d.autoPath
	}
	named := d.remote || d.fresh
	a.modal(c, &d.open, func() {
		dialogBox(c).Width(dialogMedium).Children(func() {
			title := locales.WorktreeNewTitle(redact.Control(short))
			if d.fresh {
				title = locales.WorktreeNewBranchTitle()
			}
			ui.Text(c, title).Bold()
			submitted := false
			if named {
				submitted = ui.TextInput(c, &d.name).Label(locales.CommonNewBranch()).Placeholder(locales.CommonBranchName()).AutoFocus().FillWidth().Submitted()
			}
			ui.Row(c).Gap(spaceM).AlignItems(ui.Center).FillWidth().Children(func() {
				p := ui.TextInput(c, &d.path).Label(locales.CommonFolder()).Placeholder(locales.CommonFolder()).Grow(1)
				if !named {
					p.AutoFocus()
				}
				submitted = submitted || p.Submitted()
				if a.PickFolder != nil && ui.Button(c, locales.CommonChoose()).Clicked() {
					a.pickWorktreeParent(d)
				}
			})
			ui.Checkbox(c, &d.openTab, locales.WorktreeOpenTab())
			if d.err != "" {
				ui.Text(c, d.err).FontSize(textSmall).TextColor(th.Danger).Selectable()
			}
			ui.Row(c).Gap(spaceM).Children(func() {
				ui.Spacer(c)
				if ui.Button(c, locales.CommonCancel()).Clicked() {
					d.open = false
				}
				ready := strings.TrimSpace(d.path) != "" && (!named || strings.TrimSpace(d.name) != "")
				if ui.PrimaryButton(c, locales.CommonCreate()).Disabled(!ready).Clicked() || ready && submitted {
					a.addWorktree(d)
				}
			})
		})
	})
}

// pickWorktreeParent asks for the folder to put the worktree in, keeping
// the worktree's own folder name.
func (a *App) pickWorktreeParent(d *worktreeDialog) {
	pick, update := a.PickFolder, a.Update
	a.goWork(nil, func() {
		p, err := pick(locales.WorktreePickFolder())
		update(func() {
			switch {
			case err != nil:
				d.err = errText(err)
			case p != "":
				d.path = filepath.Join(p, filepath.Base(strings.TrimSpace(d.path)))
			}
		})
	})
}

func (a *App) addWorktree(d *worktreeDialog) {
	t := d.t
	path := strings.TrimSpace(d.path)
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	o := write.WorktreeOptions{Path: path, Branch: d.branch}
	switch {
	case d.fresh:
		o = write.WorktreeOptions{Path: path, NewBranch: strings.TrimSpace(d.name)}
	case d.remote:
		o = write.WorktreeOptions{Path: path, NewBranch: strings.TrimSpace(d.name), Start: d.branch, Track: true}
	}
	op, err := write.AddWorktree(t.s.Where(), o)
	var j *write.Job
	if err == nil {
		j, err = t.s.Submit(op)
	}
	if err != nil {
		d.err = errText(err)
		return
	}
	d.open = false
	if d.openTab {
		t.s.Then(j, func(err error) {
			if err == nil {
				a.Open(path)
			}
		})
	}
}

// worktreeLabel is how the sidebar lists w.
func worktreeLabel(w repo.Worktree) string {
	label := filepath.Base(w.Path)
	switch {
	case w.Branch != "":
		label += "  " + strings.TrimPrefix(w.Branch, "refs/heads/")
	case w.Detached:
		label += "  " + locales.WorktreeDetached()
	}
	switch {
	case w.Prunable:
		label += "  " + locales.WorktreeFolderMissing()
	case w.Locked:
		label += "  " + locales.WorktreeLocked()
	}
	return label
}

// worktreeMenu is what can be done with worktree w from the sidebar. The
// first is the main one, which git never removes.
func (a *App) worktreeMenu(it ui.Element, t *tab, w repo.Worktree, main bool) {
	it.ContextMenu(func(m *ui.Menu) {
		var act func()
		a.worktreeItems(m, t, w, main, &act)
		if act != nil {
			act()
		}
	})
}

// worktreeItems fills the menu of worktree w, in the sidebar or the
// worktree panel; act is set to what was chosen.
func (a *App) worktreeItems(m *ui.Menu, t *tab, w repo.Worktree, main bool, act *func()) {
	ro := t.s.Queue == nil
	if m.Item(locales.WorktreeOpenInNewTab()).Disabled(w.Current || w.Prunable).Chosen() {
		*act = func() { a.openWorktree(t, w) }
	}
	if a.NewWindow != nil && m.Item(locales.WorktreeOpenInNewWindow()).Disabled(w.Current || w.Prunable).Chosen() {
		*act = func() { a.NewWindow([]string{w.Path}) }
	}
	if m.Item(locales.WorktreeCopyPath()).Chosen() {
		*act = func() { a.clip = w.Path }
	}
	if w.Locked {
		if m.Item(locales.WorktreeUnlock()).Disabled(ro).Chosen() {
			*act = func() { a.submit(t, write.UnlockWorktree(t.s.Where(), w.Path), nil) }
		}
	} else if m.Item(locales.WorktreeLockEllipsis()).Disabled(ro || main).Chosen() {
		*act = func() { a.worktreeLockUI = worktreeLock{open: true, t: t, path: w.Path} }
	}
	if w.Prunable {
		if m.Item(locales.WorktreePruneEllipsis()).Disabled(ro || w.Locked).Chosen() {
			*act = func() { a.askPrune(t, w) }
		}
	} else if m.Item(locales.WorktreeRemoveEllipsis()).Disabled(ro || main || w.Current || w.Locked).Danger().Chosen() {
		*act = func() { a.worktreeRemoveUI = worktreeRemoval{open: true, t: t, wt: w} }
	}
}

// askPrune offers to forget a worktree whose folder is gone, which is
// what opening it would otherwise fail on.
func (a *App) askPrune(t *tab, w repo.Worktree) {
	title := locales.WorktreeMissingTitle(filepath.Base(w.Path))
	msg := locales.WorktreeGoneMessage(w.Path)
	if w.Locked {
		msg = locales.WorktreeGoneLockedMessage(w.Path)
		if w.LockReason != "" {
			msg = locales.WorktreeGoneLockedReasonMessage(w.Path, w.LockReason)
		}
		a.confirm = confirmState{open: true, title: title, message: msg, button: locales.WorktreeUnlock(), safe: true,
			run: func() { a.submit(t, write.UnlockWorktree(t.s.Where(), w.Path), nil) }}
		return
	}
	a.confirm = confirmState{open: true, title: title, message: msg, button: locales.WorktreePrune(), safe: true,
		run: func() { a.submit(t, write.PruneWorktrees(t.s.Where()), nil) }}
}

func (a *App) worktreeRemoveView(c *ui.Context) {
	d := &a.worktreeRemoveUI
	if !d.open || d.t == nil {
		return
	}
	branch := strings.TrimPrefix(d.wt.Branch, "refs/heads/")
	a.modal(c, &d.open, func() {
		dialogBox(c).Width(dialogNarrow).Children(func() {
			ui.Text(c, locales.WorktreeRemoveTitle(filepath.Base(d.wt.Path))).Bold()
			ui.Text(c, locales.WorktreeRemoveFolder(d.wt.Path)).Selectable()
			if branch != "" {
				ui.Checkbox(c, &d.withBranch, locales.WorktreeAlsoDeleteBranch(redact.Control(branch)))
			}
			ui.Checkbox(c, &d.force, locales.WorktreeRemoveForce())
			ui.Row(c).Gap(spaceM).Children(func() {
				ui.Spacer(c)
				// The destructive choice is never the default one: Enter
				// must not delete a folder, least of all with force on.
				if ui.Button(c, locales.CommonRemove()).Clicked() {
					d.open = false
					a.removeWorktree(d)
				}
				if ui.PrimaryButton(c, locales.CommonCancel()).Clicked() {
					d.open = false
				}
			})
		})
	})
}

// removeWorktree removes the worktree, then, once it is gone, its branch
// when asked; the branch delete is backed up and undone on its own.
func (a *App) removeWorktree(d *worktreeRemoval) {
	t, w := d.t, d.t.s.Where()
	j, err := t.s.Submit(write.RemoveWorktree(w, d.wt.Path, d.force))
	if err != nil {
		a.notice = errText(err)
		return
	}
	if d.withBranch && d.wt.Branch != "" {
		branch, head := d.wt.Branch, d.wt.Head
		// Once the worktree went: before, the delete fails too, and its
		// error hides the one that says why.
		t.s.Then(j, func(err error) {
			if err == nil {
				a.submit(t, write.DeleteBranch(w, branch, head, true, "", t.s.Seen()), nil)
			}
		})
	}
}

func (a *App) worktreeLockView(c *ui.Context) {
	d := &a.worktreeLockUI
	if !d.open || d.t == nil {
		return
	}
	a.modal(c, &d.open, func() {
		dialogBox(c).Width(dialogNarrow).Children(func() {
			ui.Text(c, locales.WorktreeLockTitle(filepath.Base(d.path))).Bold()
			in := ui.TextInput(c, &d.reason).Label(locales.WorktreeReason()).Placeholder(locales.WorktreeReasonPlaceholder()).AutoFocus().FillWidth()
			ui.Row(c).Gap(spaceM).Children(func() {
				ui.Spacer(c)
				if ui.Button(c, locales.CommonCancel()).Clicked() {
					d.open = false
				}
				if ui.PrimaryButton(c, locales.WorktreeLock()).Clicked() || in.Submitted() {
					d.open = false
					a.submit(d.t, write.LockWorktree(d.t.s.Where(), d.path, d.reason), nil)
				}
			})
		})
	})
}
