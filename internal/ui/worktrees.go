package ui

import (
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/redact"
	"ramus/internal/git/repo"
	"ramus/internal/git/write"
)

// worktreeDialog makes a worktree of a branch: a local one checked out
// there, or a remote one with a new local branch tracking it.
type worktreeDialog struct {
	open bool
	t    *tab
	// branch is the full name of the branch the worktree is for.
	branch string
	remote bool
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

// worktreePath is the folder suggested for a worktree of branch: beside
// the main one, named after both.
func worktreePath(t *tab, branch string) string {
	main := t.s.Path
	if wts := t.s.State.Worktrees; len(wts) > 0 && !wts[0].Bare {
		main = wts[0].Path
	}
	name := strings.NewReplacer("/", "-", `\`, "-").Replace(branch)
	return filepath.Join(filepath.Dir(main), filepath.Base(main)+"-"+name)
}

func (a *App) worktreeView(c *ui.Context) {
	d := &a.worktreeUI
	if !d.open || d.t == nil {
		return
	}
	th := c.Theme()
	short := strings.TrimPrefix(strings.TrimPrefix(d.branch, "refs/heads/"), "refs/remotes/")
	ui.Modal(c, &d.open, func() {
		ui.Column(c).Width(520).Gap(12).Padding(20).Children(func() {
			ui.Text(c, "New Worktree for "+redact.Control(short)).Bold()
			submitted := false
			if d.remote {
				submitted = ui.TextInput(c, &d.name).Label("New branch").Placeholder("Branch name").AutoFocus().FillWidth().Submitted()
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).FillWidth().Children(func() {
				p := ui.TextInput(c, &d.path).Label("Folder").Placeholder("Folder").Grow(1)
				if !d.remote {
					p.AutoFocus()
				}
				submitted = submitted || p.Submitted()
				if a.PickFolder != nil && ui.Button(c, "Choose…").Clicked() {
					a.pickWorktreeParent(d)
				}
			})
			ui.Checkbox(c, &d.openTab, "Open in a new tab")
			if d.err != "" {
				ui.Text(c, d.err).FontSize(12).TextColor(th.Danger).Selectable()
			}
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				if ui.Button(c, "Cancel").Clicked() {
					d.open = false
				}
				ready := strings.TrimSpace(d.path) != "" && (!d.remote || strings.TrimSpace(d.name) != "")
				if ui.PrimaryButton(c, "Create").Disabled(!ready).Clicked() || ready && submitted {
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
	go func() {
		p, err := pick("Folder for the Worktree")
		update(func() {
			switch {
			case err != nil:
				d.err = errText(err)
			case p != "":
				d.path = filepath.Join(p, filepath.Base(strings.TrimSpace(d.path)))
			}
		})
	}()
}

func (a *App) addWorktree(d *worktreeDialog) {
	t := d.t
	path := strings.TrimSpace(d.path)
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	o := write.WorktreeOptions{Path: path, Branch: d.branch}
	if d.remote {
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
		label += "  detached"
	}
	switch {
	case w.Prunable:
		label += "  folder missing"
	case w.Locked:
		label += "  locked"
	}
	return label
}

// worktreeMenu is what can be done with worktree w from the sidebar. The
// first is the main one, which git never removes.
func (a *App) worktreeMenu(it ui.Element, t *tab, w repo.Worktree, main bool) {
	it.ContextMenu(func(m *ui.Menu) {
		ro := t.s.Queue == nil
		var act func()
		if m.Item("Open in New Tab").Disabled(w.Current || w.Prunable).Chosen() {
			act = func() { a.Open(w.Path) }
		}
		if a.NewWindow != nil && m.Item("Open in New Window").Disabled(w.Current || w.Prunable).Chosen() {
			act = func() { a.NewWindow([]string{w.Path}) }
		}
		if m.Item("Copy Path").Chosen() {
			act = func() { a.clip = w.Path }
		}
		if w.Locked {
			if m.Item("Unlock").Disabled(ro).Chosen() {
				act = func() { a.submit(t, write.UnlockWorktree(t.s.Where(), w.Path), nil) }
			}
		} else if m.Item("Lock…").Disabled(ro || main).Chosen() {
			act = func() { a.worktreeLockUI = worktreeLock{open: true, t: t, path: w.Path} }
		}
		if w.Prunable {
			if m.Item("Prune…").Disabled(ro || w.Locked).Chosen() {
				act = func() { a.askPrune(t, w) }
			}
		} else if m.Item("Remove…").Disabled(ro || main || w.Current || w.Locked).Danger().Chosen() {
			act = func() { a.worktreeRemoveUI = worktreeRemoval{open: true, t: t, wt: w} }
		}
		if act != nil {
			act()
		}
	})
}

// askPrune offers to forget a worktree whose folder is gone, which is
// what opening it would otherwise fail on.
func (a *App) askPrune(t *tab, w repo.Worktree) {
	msg := "The folder " + w.Path + " is gone. Pruning forgets every worktree whose folder is gone; their branches stay."
	if w.Locked {
		msg = "The folder " + w.Path + " is gone, and the worktree is locked"
		if w.LockReason != "" {
			msg += " (" + w.LockReason + ")"
		}
		msg += ": its folder may be on a drive not connected now. Unlock it to prune it."
		a.confirm = confirmState{open: true, title: "Worktree " + filepath.Base(w.Path) + " is missing", message: msg, button: "Unlock",
			run: func() { a.submit(t, write.UnlockWorktree(t.s.Where(), w.Path), nil) }}
		return
	}
	a.confirm = confirmState{open: true, title: "Worktree " + filepath.Base(w.Path) + " is missing", message: msg, button: "Prune",
		run: func() { a.submit(t, write.PruneWorktrees(t.s.Where()), nil) }}
}

func (a *App) worktreeRemoveView(c *ui.Context) {
	d := &a.worktreeRemoveUI
	if !d.open || d.t == nil {
		return
	}
	branch := strings.TrimPrefix(d.wt.Branch, "refs/heads/")
	ui.Modal(c, &d.open, func() {
		ui.Column(c).Width(480).Gap(12).Padding(20).Children(func() {
			ui.Text(c, "Remove worktree "+filepath.Base(d.wt.Path)+"?").Bold()
			ui.Text(c, "Its folder "+d.wt.Path+" is deleted.").Selectable()
			if branch != "" {
				ui.Checkbox(c, &d.withBranch, "Also delete branch "+redact.Control(branch))
			}
			ui.Checkbox(c, &d.force, "Remove even with changes or untracked files")
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				if ui.Button(c, "Cancel").Clicked() {
					d.open = false
				}
				if ui.PrimaryButton(c, "Remove").Clicked() {
					d.open = false
					a.removeWorktree(d)
				}
			})
		})
	})
}

// removeWorktree removes the worktree, then its branch when asked; the
// branch delete is backed up and undone on its own, and fails while the
// worktree is still there.
func (a *App) removeWorktree(d *worktreeRemoval) {
	t, w := d.t, d.t.s.Where()
	a.submit(t, write.RemoveWorktree(w, d.wt.Path, d.force), nil)
	if d.withBranch && d.wt.Branch != "" {
		a.submit(t, write.DeleteBranch(w, d.wt.Branch, d.wt.Head, true, "", t.s.Seen()), nil)
	}
}

func (a *App) worktreeLockView(c *ui.Context) {
	d := &a.worktreeLockUI
	if !d.open || d.t == nil {
		return
	}
	ui.Modal(c, &d.open, func() {
		ui.Column(c).Width(420).Gap(12).Padding(20).Children(func() {
			ui.Text(c, "Lock worktree "+filepath.Base(d.path)).Bold()
			in := ui.TextInput(c, &d.reason).Label("Reason").Placeholder("Reason (optional)").AutoFocus().FillWidth()
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				if ui.Button(c, "Cancel").Clicked() {
					d.open = false
				}
				if ui.PrimaryButton(c, "Lock").Clicked() || in.Submitted() {
					d.open = false
					a.submit(d.t, write.LockWorktree(d.t.s.Where(), d.path, d.reason), nil)
				}
			})
		})
	})
}
