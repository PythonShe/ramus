package ui

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/actions"
	"ramus/internal/git/pathsafe"
	"ramus/internal/session"
	"ramus/internal/settings"
	"ramus/internal/tools"
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
		a.notice = act.Name + ": " + errText(err)
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
		ui.Column(c).Width(480).Gap(12).Padding(20).Children(func() {
			ui.Text(c, p.action.Name).Bold()
			for i, q := range p.questions {
				ui.Field(c, q, func() {
					in := ui.TextInput(c, &p.answers[i])
					if i == 0 {
						in.AutoFocus()
					}
					if in.Submitted() && i == len(p.questions)-1 {
						run = true
					}
				})
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "Cancel").Clicked() {
					p.open = false
				}
				if ui.PrimaryButton(c, "Run").Clicked() {
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

// editor is the tool files open in: the user's own, else the first found.
func (a *App) editor() (tools.Tool, bool) {
	for _, tl := range a.palette.tools {
		if tl.Kind == tools.Editor {
			return tl, true
		}
	}
	return tools.Tool{}, false
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
			go func() {
				abs, err := pathsafe.Target(root, rel)
				if err == nil {
					err = fn(abs)
				}
				if err != nil {
					update(func() { a.notice = errText(err) })
				}
			}()
		}
	}
	ed, ok := a.editor()
	if m.Item("Open in Editor").Disabled(!ok || root == "").Chosen() {
		*act = withPath(func(abs string) error { return ed.Open(abs, 0) })
	}
	if m.Item("Reveal in File Manager").Disabled(root == "").Chosen() {
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
	th := c.Theme()
	log := t.s.Log
	if len(log) == 0 {
		ui.Column(c).Fill().Center().Children(func() {
			ui.Text(c, "Commands the app runs here show up in this log").TextColor(th.TextMuted)
		})
		return
	}
	t.logList.Selected = &t.logChosen
	t.logList.Label = func(i int) string { return log[len(log)-1-i].Name }
	ui.Split(c, &t.listW, func() {
		ui.List(c, &t.logList, len(log), func(i int) {
			e := log[len(log)-1-i]
			ui.Column(c).Padding(6, 8).Children(func() {
				ui.Row(c).Gap(8).Children(func() {
					ui.Text(c, e.Name).SingleLine().Grow(1)
					ui.Text(c, entryStatus(e)).FontSize(12).TextColor(statusColor(th, e))
				})
				ui.Text(c, e.Started.Format(time.TimeOnly)).FontSize(12).TextColor(th.TextMuted)
			})
		}).Fill()
	}, func() {
		k := len(log) - 1 - t.logChosen
		if t.logChosen < 0 || k < 0 || k >= len(log) {
			return
		}
		e := log[k]
		ui.Column(c).Fill().Padding(12).Gap(8).Children(func() {
			ui.Text(c, e.Command).Font("monospace").FontSize(12).TextColor(th.TextMuted).Selectable()
			if e.Dropped > 0 {
				ui.Text(c, fmt.Sprintf("%d earlier lines not kept", e.Dropped)).FontSize(12).TextColor(th.TextMuted)
			}
			ui.List(c, &t.outList, len(e.Output), func(i int) {
				ui.Text(c, e.Output[i]).Font("monospace").FontSize(12).Selectable()
			}).Grow(1)
			if e.Err != nil {
				ui.Text(c, errText(e.Err)).TextColor(th.Danger).Selectable()
			}
		})
	}).Fill()
}

func entryStatus(e *session.LogEntry) string {
	switch {
	case e.Running:
		return "running"
	case e.Err != nil:
		return "failed"
	}
	return "done in " + e.Ended.Sub(e.Started).Round(time.Millisecond).String()
}

func statusColor(th *ui.Theme, e *session.LogEntry) ui.Color {
	switch {
	case e.Running:
		return th.Accent
	case e.Err != nil:
		return th.Danger
	}
	return th.TextMuted
}
