package ui

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"ramus/internal/tools"
)

type paletteState struct {
	open  bool
	query string
	tools []tools.Tool
	found bool
}

// command is one entry of the palette.
type command struct {
	label string
	run   func()
}

func (a *App) commands() []command {
	cmds := []command{
		{"Open Repository…", a.pickAndOpen},
		{"Clone Repository…", func() { a.clone.open = true }},
		{"New Repository…", a.initAndOpen},
		{"Settings…", a.openSettings},
		{"Repositories…", a.openManager},
	}
	t := a.current()
	if t != nil {
		cmds = append(cmds, command{"Close Tab", func() { a.Close(a.active) }})
		if t.s.State.Restricted {
			cmds = append(cmds, command{"Trust This Repository", func() { t.s.Trust() }})
		}
		dir := t.s.Path
		for _, tl := range a.palette.tools {
			cmds = append(cmds, command{"Open in " + tl.Name, func() {
				if err := tl.Open(dir, 0); err != nil {
					a.notice = errText(err)
				}
			}})
		}
		cmds = append(cmds, command{"Reveal in File Manager", func() { go tools.Reveal(dir) }})
		cmds = append(cmds, command{"Show Command Log", func() { t.sel = "log" }})
		cmds = append(cmds, command{"Show Snapshots", func() { t.sel = "snapshots" }})
		if q := t.s.Queue; q != nil {
			cmds = append(cmds, command{"Snapshot Now", func() {
				if _, err := t.s.SnapshotNow(false); err != nil {
					a.notice = errText(err)
				}
			}})
			undo, redo := q.History.Next()
			if undo != "" {
				cmds = append(cmds, command{"Undo " + undo, func() { a.undo(t, false) }})
			}
			if redo != "" {
				cmds = append(cmds, command{"Redo " + redo, func() { a.undo(t, true) }})
			}
		}
		for _, x := range a.Settings.Actions {
			if x.On == "" {
				cmds = append(cmds, command{x.Name, func() { a.startAction(t, x, varsFor(t)) }})
			}
		}
	}
	for i, other := range a.tabs {
		cmds = append(cmds, command{"Go to " + a.title(other), func() { a.active = i }})
	}
	if a.Library != nil {
		for _, g := range a.Library.Groups() {
			paths := g.Paths
			cmds = append(cmds, command{"Open Group " + g.Name, func() {
				for _, p := range paths {
					a.Open(p)
				}
			}})
		}
		for _, r := range a.Library.Recent(a.Settings.WithDefaults().RecentLimit) {
			p := r.Path
			cmds = append(cmds, command{"Open " + r.Name + " (" + r.Path + ")", func() { a.Open(p) }})
		}
	}
	return cmds
}

// match keeps the commands holding every word of the query.
func match(cmds []command, query string) []command {
	words := strings.Fields(strings.ToLower(query))
	var out []command
	for _, c := range cmds {
		l := strings.ToLower(c.label)
		ok := true
		for _, w := range words {
			if !strings.Contains(l, w) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, c)
		}
	}
	return out
}

// paletteView is the command palette: Cmd+Shift+P or Cmd+K, type, Enter.
func (a *App) paletteView(c *ui.Context) {
	p := &a.palette
	if !p.open {
		return
	}
	cmds := match(a.commands(), p.query)
	var run func()
	ui.Modal(c, &p.open, func() {
		cb := ui.ComboboxBase(c, &p.query)
		cb.Input.AutoFocus().Placeholder("Type a command").Width(520)
		cb.Popup(func(panel ui.Element) {
			for _, cmd := range cmds {
				cb.Item(cmd.label).Padding(6, 10).Children(func() { ui.Text(c, cmd.label).SingleLine() })
			}
		})
		if label, ok := cb.Chosen(); ok {
			for _, cmd := range cmds {
				if cmd.label == label {
					run = cmd.run
				}
			}
		}
	})
	if run != nil {
		p.open, p.query = false, ""
		run()
	}
	if !p.open {
		p.query = ""
	}
}
