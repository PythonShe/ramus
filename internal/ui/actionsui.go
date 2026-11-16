package ui

import (
	"encoding/hex"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/actions"
	"ramus/internal/git/pathsafe"
	"ramus/internal/session"
	"ramus/internal/settings"
	"ramus/internal/tools"
	"ramus/locales"
)

type promptState struct {
	open      bool
	tab       *tab
	action    settings.Action
	vars      actions.Vars
	questions []string
	answers   []string
}

// varsFor is what an action started in t runs on.
func varsFor(t *tab) actions.Vars {
	st := t.s.State
	v := actions.Vars{Branch: strings.TrimPrefix(st.Head.Target, "refs/heads/"), Commit: hex.EncodeToString(st.Head.OID)}
	if r := t.s.Repo; r != nil {
		v.Repo = r.WorkTree
	}
	return v
}

// startAction asks the action's questions, if any, then runs it.
func (a *App) startAction(t *tab, act settings.Action, v actions.Vars) {
	qs := actions.Prompts(act.Command)
	if len(qs) == 0 {
		a.runAction(t, act, v, nil)
		return
	}
	a.prompt = promptState{open: true, tab: t, action: act, vars: v, questions: qs, answers: make([]string, len(qs))}
}

func (a *App) runAction(t *tab, act settings.Action, v actions.Vars, answers map[string]string) {
	argv, err := actions.Expand(act.Command, v, answers)
	if err != nil {
		a.notice = locales.ActionsError(act.Name, errText(err))
		return
	}
	t.s.RunAction(act.Name, argv)
	t.sel = "log"
	t.logChosen = 0 // newest first
}

func (a *App) promptView(c *ui.Context) {
	p := &a.prompt
	if !p.open {
		return
	}
	run := false
	ui.Modal(c, &p.open, func() {
		dialogBox(c).Width(dialogNarrow).Children(func() {
			ui.Text(c, p.action.Name).Bold()
			for i, q := range p.questions {
				ui.Field(c, q, func() {
					in := ui.TextInput(c, &p.answers[i]).Label(q)
					if i == 0 {
						in.AutoFocus()
					}
					if in.Submitted() && i == len(p.questions)-1 {
						run = true
					}
				})
			}
			ui.Row(c).Gap(spaceM).Justify(ui.End).Children(func() {
				if ui.Button(c, locales.CommonCancel()).Clicked() {
					p.open = false
				}
				if ui.PrimaryButton(c, locales.ActionsRun()).Clicked() {
					run = true
				}
			})
		})
	})
	if run && p.open {
		answers := map[string]string{}
		for i, q := range p.questions {
			answers[q] = p.answers[i]
		}
		p.open = false
		if p.tab != nil {
			a.runAction(p.tab, p.action, p.vars, answers)
		}
	}
}

// editor is the tool t's files open in: the one chosen for the
// repository, else the user's own, else the first found.
func (a *App) editor(t *tab) (tools.Tool, bool) {
	if name := t.prefs.editor; name != "" {
		for _, tl := range a.palette.tools {
			if tl.Kind == tools.Editor && tl.Name == name {
				return tl, true
			}
		}
	}
	for _, tl := range a.palette.tools {
		if tl.Kind == tools.Editor {
			return tl, true
		}
	}
	return tools.Tool{}, false
}

// editorMenu chooses the editor a repository's files open in.
func (a *App) editorMenu(m *ui.Menu, t *tab, act *func()) {
	var eds []string
	for _, tl := range a.palette.tools {
		if tl.Kind == tools.Editor && !slices.Contains(eds, tl.Name) {
			eds = append(eds, tl.Name)
		}
	}
	if len(eds) < 2 {
		return
	}
	a.loadRefPrefs(t) // a tab never shown has not read them
	path, cur := t.s.Path, t.prefs.editor
	if !slices.Contains(eds, cur) {
		cur = ""
	}
	m.Submenu(locales.ActionsOpenFilesIn(), func(sub *ui.Menu) {
		if sub.Item(locales.ActionsDefaultEditor(toolName(eds[0]))).Checked(cur == "").Chosen() {
			*act = func() { a.setEditor(path, "") }
		}
		sub.Separator()
		for _, name := range eds {
			if sub.Item(toolName(name)).Checked(cur == name).Chosen() {
				*act = func() { a.setEditor(path, name) }
			}
		}
	})
}

// fileMenu is the context menu of a changed file. Each choice checks
// the path first, off the main thread: a path from git may lead out of
// the work tree through a symlink, and nothing is opened there.
func (a *App) fileMenu(m *ui.Menu, t *tab, rel string, act *func()) {
	root := ""
	if r := t.s.Repo; r != nil {
		root = r.WorkTree
	}
	withPath := func(fn func(abs string) error) func() {
		update := a.Update
		return func() {
			a.goWork(t, func() {
				abs, err := pathsafe.Target(root, rel)
				if err == nil {
					err = fn(abs)
				}
				if err != nil {
					update(func() { a.tell(t, errText(err)) })
				}
			})
		}
	}
	if m.Item(locales.ActionsShowFileHistory()).Disabled(root == "").Chosen() {
		*act = func() { a.showFile(t, rel, false) }
	}
	if m.Item(locales.ActionsBlame()).Disabled(root == "").Chosen() {
		*act = func() { a.showFile(t, rel, true) }
	}
	ed, ok := a.editor(t)
	if m.Item(locales.ActionsOpenInEditor()).Disabled(!ok || root == "").Chosen() {
		*act = withPath(func(abs string) error { return ed.Open(abs, 0) })
	}
	if m.Item(locales.ActionsReveal()).Disabled(root == "").Chosen() {
		*act = withPath(tools.Reveal)
	}
	var mine []settings.Action
	for _, x := range a.Settings.Actions {
		if x.On == "file" {
			mine = append(mine, x)
		}
	}
	if len(mine) > 0 {
		m.Separator()
		for _, x := range mine {
			if m.Item(x.Name).Disabled(root == "").Chosen() {
				x, update := x, a.Update
				*act = withPath(func(abs string) error {
					update(func() {
						v := varsFor(t)
						v.File = abs
						a.startAction(t, x, v)
					})
					return nil
				})
			}
		}
	}
}

// logView lists what the app ran in the repository, and the output of
// the entry chosen.
func (a *App) logView(c *ui.Context, t *tab) {
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(barPadY, insetX).Children(func() {
			ui.Segmented(c, &t.gitLog.mode, locales.ActionsActions(), locales.ActionsGitCommands())
		})
		if t.gitLog.mode == 1 {
			a.gitLogView(c, t)
		} else {
			a.actionLogView(c, t)
		}
	})
}

// actionLogView lists the writes and actions run for the user, and the
// output of the entry chosen as it came.
func (a *App) actionLogView(c *ui.Context, t *tab) {
	th := c.Theme()
	log := t.s.Log
	if len(log) == 0 {
		emptyState(c, locales.ActionsEmpty())
		return
	}
	t.logList.Selected = &t.logChosen
	t.logList.Label = func(i int) string { return log[len(log)-1-i].Name }
	ui.Split(c, &t.listW, func() {
		ui.List(c, &t.logList, len(log)).Rows(func(r ui.ListRow) {
			e, k := log[len(log)-1-r.Index], rowInk(r)
			ui.Column(c).Padding(rowTallPadY, insetX).Children(func() {
				ui.Row(c).Gap(spaceM).Children(func() {
					ui.Text(c, e.Name).SingleLine().Grow(1)
					ui.Text(c, entryStatus(e)).FontSize(textSmall).TextColor(statusColor(k, e))
				})
				ui.Text(c, e.Started.Format(time.TimeOnly)).FontSize(textSmall).TextColor(k.Muted)
			})
		}).Label(locales.ActionsCommandsRun()).Fill()
	}, func() {
		k := len(log) - 1 - t.logChosen
		if t.logChosen < 0 || k < 0 || k >= len(log) {
			return
		}
		e := log[k]
		ui.Column(c).Fill().Padding(spaceL).Gap(spaceM).Children(func() {
			ui.Text(c, e.Command).Font("monospace").FontSize(textSmall).TextColor(th.TextMuted).Selectable()
			if e.Dropped > 0 {
				ui.Text(c, locales.ActionsLinesDropped(e.Dropped)).FontSize(textSmall).TextColor(th.TextMuted)
			}
			ui.List(c, &t.outList, len(e.Output), func(i int) {
				ui.Text(c, e.Output[i]).Font("monospace").FontSize(textSmall).Selectable()
			}).Label(locales.ActionsOutput()).Grow(1)
			if e.Err != nil {
				ui.Text(c, errText(e.Err)).TextColor(th.Danger).Selectable()
			}
		})
	}).Fill()
}

func entryStatus(e *session.LogEntry) string {
	switch {
	case e.Running:
		return locales.ActionsRunning()
	case e.Err != nil:
		return locales.ActionsFailed()
	}
	return locales.ActionsDoneIn(e.Ended.Sub(e.Started).Round(time.Millisecond).String())
}

func statusColor(k ink, e *session.LogEntry) ui.Color {
	switch {
	case e.Running:
		return k.Accent
	case e.Err != nil:
		return k.Danger
	}
	return k.Muted
}
