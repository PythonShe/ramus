package ui

import (
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/write"
	"ramus/internal/tools"
)

type paletteState struct {
	open  bool
	query string
	tools []tools.Tool
	found bool
}

// command is one entry of the palette. One with an id can be given a
// shortcut; a hidden one is run by its shortcut alone.
type command struct {
	id     string
	label  string
	run    func()
	hidden bool
}

// cmd is the command id, under the name bindable gives it.
func cmd(id string, run func()) command {
	for _, b := range bindable {
		if b.id == id {
			return command{id: id, label: b.name, run: run}
		}
	}
	return command{id: id, label: id, run: run}
}

func (a *App) commands() []command {
	cmds := []command{
		{id: "palette", label: "Command Palette", run: func() { a.palette.open = true }, hidden: true},
		cmd("open", a.pickAndOpen),
		cmd("clone", func() { a.clone.open = true }),
		cmd("new", a.initAndOpen),
		cmd("settings", a.openSettings),
		cmd("repositories", a.openManager),
		cmd("library-stats", a.openLibStats),
	}
	t := a.current()
	if t != nil {
		cmds = append(cmds, a.tabCommands(t)...)
	}
	for i, other := range a.tabs {
		cmds = append(cmds, command{label: "Go to " + a.title(other), run: func() { a.active = i }})
		if i < 9 {
			id := "tab-" + strconv.Itoa(i+1)
			cmds = append(cmds, command{id: id, label: id, run: func() { a.active = i }, hidden: true})
		}
	}
	if a.Library != nil {
		for _, g := range a.Library.Groups() {
			paths := g.Paths
			cmds = append(cmds, command{label: "Open Group " + g.Name, run: func() {
				for _, p := range paths {
					a.Open(p)
				}
			}})
		}
		for _, r := range a.Library.Recent(a.Settings.WithDefaults().RecentLimit) {
			p := r.Path
			cmds = append(cmds, command{label: "Open " + r.Name + " (" + r.Path + ")", run: func() { a.Open(p) }})
		}
	}
	return cmds
}

// tabCommands are the commands on the repository of tab t.
func (a *App) tabCommands(t *tab) []command {
	cmds := []command{
		cmd("close-tab", func() { a.Close(a.active) }),
		cmd("refresh", func() { a.reload(t) }),
	}
	if t.s.State.Restricted {
		cmds = append(cmds, cmd("trust", func() { t.s.Trust() }))
	}
	dir := t.s.Path
	for _, tl := range a.palette.tools {
		cmds = append(cmds, command{id: toolID(tl.Name), label: "Open in " + tl.Name, run: func() {
			if err := tl.Open(dir, 0); err != nil {
				a.notice = errText(err)
			}
		}})
	}
	cmds = append(cmds,
		cmd("reveal", func() { go tools.Reveal(dir) }),
		cmd("show-changes", func() { t.sel = "changes" }),
		cmd("show-history", func() { t.sel = "history" }),
		cmd("show-reflog", func() { t.sel = "reflog" }),
		cmd("show-stats", func() { t.sel = "stats" }),
		cmd("lost-commits", func() { t.sel, t.reflog.lostShown = "reflog", 1 }),
		cmd("issue-links", func() { a.openLinks(t) }),
	)
	cmds = append(cmds, a.hidingCommands()...)
	if a.Settings.FetchMinutes > 0 && t.prefs.loaded {
		label, off := "Stop Fetching This Repository in the Background", true
		if t.prefs.fetchOff {
			label, off = "Fetch This Repository in the Background", false
		}
		path := t.s.Path
		cmds = append(cmds, command{label: label, run: func() { a.setFetchOff(path, off) }})
	}
	cmds = append(cmds,
		cmd("show-log", func() { t.sel = "log" }),
		cmd("show-snapshots", func() { t.sel = "snapshots" }),
		cmd("show-refs", func() { t.sel = "refs" }),
		cmd("show-stashes", func() { t.sel = "stashes" }),
	)
	if q := t.s.Queue; q != nil {
		cmds = append(cmds, a.writeCommands(t)...)
		cmds = append(cmds, cmd("snapshot", func() {
			if _, err := t.s.SnapshotNow(false); err != nil {
				a.notice = errText(err)
			}
		}))
		// The shortcuts try whenever pressed, as the last write may be
		// about to finish; the palette lists what there is to undo.
		undo, redo := q.History.Next()
		u, r := cmd("undo", func() { a.undo(t, false) }), cmd("redo", func() { a.undo(t, true) })
		u.label, u.hidden = "Undo "+undo, undo == ""
		r.label, r.hidden = "Redo "+redo, redo == ""
		cmds = append(cmds, u, r)
	}
	for _, x := range a.Settings.Actions {
		if x.On == "" {
			cmds = append(cmds, command{id: actionID(x.Name), label: x.Name, run: func() { a.startAction(t, x, varsFor(t)) }})
		}
	}
	return cmds
}

// writeCommands are the commands that change the repository of tab t.
func (a *App) writeCommands(t *tab) []command {
	w := t.s.Where()
	head := t.s.State.Head
	cmds := []command{
		cmd("stash", func() { a.openStash(t) }),
		cmd("clean", func() { a.openClean(t) }),
		cmd("fetch", func() {
			op, err := write.Fetch(w, "", t.refs.prune)
			a.submit(t, op, err)
		}),
	}
	if a.PickFile != nil {
		cmds = append(cmds, cmd("apply-patch", func() { a.applyPatchFile(t) }))
	}
	if commitReady(t) {
		cmds = append(cmds, cmd("commit", func() { a.startCommit(t, false) }))
		if !head.Detached {
			cmds = append(cmds, cmd("commit-push", func() { a.startCommit(t, true) }))
		}
	}
	if m := t.cv.model; m != nil {
		if es := m.files[secUnstaged]; len(es) > 0 {
			cmds = append(cmds, cmd("stage-all", func() { a.stageEntries(t, m, secUnstaged, es) }))
		}
		if es := m.files[secStaged]; len(es) > 0 {
			cmds = append(cmds, cmd("unstage-all", func() { a.stageEntries(t, m, secStaged, es) }))
		}
	}
	if !head.Detached && !head.Unborn {
		for _, p := range []struct {
			id   string
			mode write.PullMode
		}{{"pull-merge", write.PullMerge}, {"pull-rebase", write.PullRebase}, {"pull-ff", write.PullFFOnly}} {
			cmds = append(cmds, cmd(p.id, func() {
				op, err := write.Pull(w, p.mode, t.s.LocalBranches(), t.s.Seen())
				a.submit(t, op, err)
			}))
		}
		cmds = append(cmds, cmd("push", func() { a.submit(t, write.Push(w, write.PushOptions{}), nil) }))
	}
	if len(head.OID) > 0 {
		cmds = append(cmds,
			cmd("new-branch", func() {
				a.branchUI = branchDialog{open: true, t: t, commit: hex.EncodeToString(head.OID), checkout: true}
			}),
			cmd("new-tag", func() { a.tagDialog(t, "HEAD") }),
		)
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
		cb.Input.AutoFocus().Placeholder("Type a command").Label("Command").Width(520)
		cb.Popup(func(panel ui.Element) {
			km := a.keys()
			for _, cmd := range cmds {
				if cmd.hidden {
					continue
				}
				cb.Item(cmd.label).Padding(6, 10).Gap(16).Children(func() {
					ui.Text(c, cmd.label).SingleLine().Grow(1)
					if ks := km.byID[cmd.id]; cmd.id != "" && len(ks) > 0 {
						ui.Text(c, ks[0].label()).FontSize(12).TextColor(c.Theme().TextMuted)
					}
				})
			}
		})
		if label, ok := cb.Chosen(); ok {
			for _, cmd := range cmds {
				if cmd.label == label && !cmd.hidden {
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
