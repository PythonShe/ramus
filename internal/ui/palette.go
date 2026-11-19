package ui

import (
	"encoding/hex"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/redact"
	"ramus/internal/git/write"
	"ramus/internal/tools"
	"ramus/locales"
)

type paletteState struct {
	open  bool
	query string
	tools []tools.Tool
	found bool
	// auto names the editor and the terminal the app takes when the
	// settings name none, by kind.
	auto [2]string
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
			return command{id: id, label: b.name(), run: run}
		}
	}
	return command{id: id, label: id, run: run}
}

// paletteTitle names tab t in the palette, which tells its commands by
// their labels: by its folder's name, with as much of the path before
// it as tells it from another tab of that name.
func (a *App) paletteTitle(t *tab) string {
	title := a.title(t)
	clash := func(name string, n int) bool {
		for _, o := range a.tabs {
			if o != t && a.title(o) == title && tail(o.s.Path, n) == name {
				return true
			}
		}
		return false
	}
	for n := 1; n < 8; n++ {
		if name := tail(t.s.Path, n); !clash(name, n) {
			return name
		}
	}
	return t.s.Path
}

// tail is the last n elements of path.
func tail(path string, n int) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	return strings.Join(parts[max(0, len(parts)-n):], "/")
}

// paletteBranches caps the branch commands the palette lists for one
// query.
const paletteBranches = 50

func (a *App) commands(query string) []command {
	cmds := []command{
		{id: "palette", label: locales.KeysPalette(), run: func() { a.palette.open = true }, hidden: true},
		cmd("open", a.pickAndOpen),
		cmd("clone", func() { a.clone.open = true }),
		cmd("new", a.initAndOpen),
		cmd("settings", a.openSettings),
		cmd("keyboard-shortcuts", a.openKeys),
		cmd("repositories", a.openManager),
		cmd("library-stats", a.openLibStats),
	}
	if a.Crash != nil {
		dir := a.Crash.Dir()
		cmds = append(cmds, cmd("crash-reports", func() { a.goOpen(nil, tools.Reveal, dir) }))
	}
	cmds = append(cmds, cmd("diagnostics", a.openDiagnostics))
	if a.ShowAbout != nil {
		cmds = append(cmds, cmd("about", a.ShowAbout))
	}
	if a.NewWindow != nil {
		cmds = append(cmds, cmd("new-window", func() { a.NewWindow(nil) }))
	}
	cmds = append(cmds, cmd("find", a.focusFind))
	if n := len(a.tabs); n > 1 {
		cmds = append(cmds,
			cmd("next-tab", func() { a.active = (a.active + 1) % n }),
			cmd("previous-tab", func() { a.active = (a.active - 1 + n) % n }))
	}
	t := a.current()
	if t != nil {
		cmds = append(cmds, cmd("switch-repository", a.openRepoPicker))
		cmds = append(cmds, a.tabCommands(t, query)...)
	}
	for i, other := range a.tabs {
		// The first nine carry the id of their shortcut, which the
		// palette then shows beside them.
		id := ""
		if i < 9 {
			id = "tab-" + strconv.Itoa(i+1)
		}
		cmds = append(cmds, command{id: id, label: locales.PaletteGoTo(a.paletteTitle(other)), run: func() { a.active = i }})
	}
	if a.Library != nil {
		for _, g := range a.Library.Groups() {
			paths := g.Paths
			cmds = append(cmds, command{label: locales.PaletteOpenGroup(g.Name), run: func() {
				for _, p := range paths {
					a.Open(p)
				}
			}})
		}
		for _, r := range a.Library.Recent(a.Settings.WithDefaults().RecentLimit) {
			p := r.Path
			cmds = append(cmds, command{label: locales.PaletteOpenRecent(r.Name, r.Path), run: func() { a.Open(p) }})
		}
	}
	return cmds
}

// tabCommands are the commands on the repository of tab t.
func (a *App) tabCommands(t *tab, query string) []command {
	cmds := []command{
		cmd("close-tab", func() { a.closeTab(a.active) }),
		cmd("refresh", func() { a.reload(t) }),
	}
	if a.NewWindow != nil && len(a.tabs) > 1 {
		cmds = append(cmds, cmd("move-to-window", func() { a.moveToNewWindow(a.active) }))
	}
	if t.s.State.Restricted {
		cmds = append(cmds, cmd("trust", func() { t.s.Trust() }))
	}
	dir := t.s.Path
	for _, tl := range a.palette.tools {
		cmds = append(cmds, command{id: toolID(tl.Name), label: locales.PaletteOpenIn(toolName(tl.Name)), run: func() {
			if err := tl.Open(dir, 0); err != nil {
				a.notice = errText(err)
			}
		}})
	}
	cmds = append(cmds,
		cmd("reveal", func() { a.goOpen(t, tools.Reveal, dir) }),
		cmd("show-changes", func() { t.sel = "changes" }),
		cmd("show-history", func() { t.sel = "history" }),
		cmd("show-reflog", func() { t.sel = "reflog" }),
		cmd("show-stats", func() { t.sel = "stats" }),
		cmd("lost-commits", func() { t.sel, t.reflog.lostShown = "reflog", 1 }),
		cmd("issue-links", func() { a.openLinks(t) }),
		cmd("switch-branch", func() { a.openPicker(t) }),
	)
	cmds = append(cmds, a.hidingCommands()...)
	if a.Settings.FetchEvery() > 0 && t.prefs.loaded {
		label, off := locales.PaletteStopFetching(), true
		if t.prefs.fetchOff {
			label, off = locales.PaletteStartFetching(), false
		}
		path := t.s.Path
		cmds = append(cmds, command{label: label, run: func() { a.setFetchOff(path, off) }})
	}
	cmds = append(cmds,
		cmd("show-log", func() { t.sel = "log" }),
		cmd("show-snapshots", func() { t.sel = "snapshots" }),
		cmd("show-refs", func() { t.sel = "refs" }),
		command{label: locales.PaletteShowWorktrees(), run: func() { t.sel = sideWorktrees }},
	)
	if st := t.s.State.Status; st != nil && st.Branch.Stashes > 0 {
		cmds = append(cmds, cmd("show-stashes", func() { t.sel = "stashes" }))
	}
	if a.hasPulls(t) {
		cmds = append(cmds, cmd("show-pulls", func() { t.sel = sidePulls }))
	}
	if t.s.State.LFS {
		cmds = append(cmds, cmd("show-lfs-locks", func() { t.sel = sideLocks }))
	}
	if q := t.s.Queue; q != nil {
		cmds = append(cmds, a.writeCommands(t)...)
		cmds = append(cmds, a.branchCommands(t, query)...)
		cmds = append(cmds, cmd("snapshot", func() {
			if _, err := t.s.SnapshotNow("", false); err != nil {
				a.notice = errText(err)
			}
		}))
		// The shortcuts try whenever pressed, as the last write may be
		// about to finish; the palette lists what there is to undo.
		undo, redo := q.History.Next()
		u, r := cmd("undo", func() { a.undo(t, false) }), cmd("redo", func() { a.undo(t, true) })
		u.label, u.hidden = locales.PaletteUndo(undo), undo == ""
		r.label, r.hidden = locales.PaletteRedo(redo), redo == ""
		cmds = append(cmds, u, r)
	}
	for _, x := range a.Settings.Actions {
		if x.On == "" {
			cmds = append(cmds, command{id: actionID(x.Name), label: x.Name, run: func() { a.startAction(t, x, varsFor(t)) }})
		}
	}
	return cmds
}

// branchCommands are Switch to for the local branches the query names:
// listed only once there is a query, as a repository can have thousands.
func (a *App) branchCommands(t *tab, query string) []command {
	if strings.TrimSpace(query) == "" {
		return nil
	}
	head := t.s.State.Head
	// A word matches the name or the words around it, as match would
	// find it in the label; only the branches kept are put in words.
	words := strings.Fields(strings.ToLower(query))
	fixed := strings.ToLower(locales.PaletteSwitchTo(""))
	var cmds []command
	for _, ref := range t.sideRefs.build(t).heads {
		if len(cmds) == paletteBranches {
			break
		}
		if !head.Detached && ref == head.Target {
			continue
		}
		name := redact.Control(strings.TrimPrefix(ref, "refs/heads/"))
		lower := strings.ToLower(name)
		if !slices.ContainsFunc(words, func(w string) bool { return !strings.Contains(lower, w) && !strings.Contains(fixed, w) }) {
			cmds = append(cmds, command{label: locales.PaletteSwitchTo(name), run: func() { a.switchTo(t, ref) }})
		}
	}
	return cmds
}

// stashTop is the commit of the newest stash, "" when there is none.
func stashTop(t *tab) string {
	l := t.s.State.Refs
	if st := t.s.State.Status; l == nil || st == nil || st.Branch.Stashes == 0 {
		return ""
	}
	if i, ok := l.Find("refs/stash"); ok {
		return hex.EncodeToString(l.OID(i))
	}
	return ""
}

// writeCommands are the commands that change the repository of tab t.
func (a *App) writeCommands(t *tab) []command {
	w := t.s.Where()
	head := t.s.State.Head
	cmds := []command{
		cmd("stash", func() { a.openStash(t) }),
		command{label: locales.PaletteNewWorktree(), run: func() { a.openNewWorktree(t) }},
		cmd("clean", func() { a.openClean(t) }),
		cmd("fetch", func() {
			op, err := write.Fetch(w, "", t.refs.prune)
			a.submit(t, op, err)
		}),
		cmd("write-commit-graph", func() {
			if err := t.s.WriteCommitGraph(); err != nil {
				a.notice = errText(err)
			}
		}),
	}
	if top := stashTop(t); top != "" {
		cmds = append(cmds, command{label: locales.PaletteStashPop(), run: func() {
			op, err := write.StashPop(w, write.StashEntry{N: 0, Commit: top}, false)
			a.submit(t, op, err)
		}})
	}
	if a.PickFile != nil {
		cmds = append(cmds, cmd("apply-patch", func() { a.applyPatchFile(t) }))
	}
	if ready, why := commitWhyNot(t); ready {
		cmds = append(cmds, cmd("commit", func() { a.startCommit(t, commitRun{}) }))
		if !head.Detached {
			upstream, pushed := amendsPushed(t)
			cmds = append(cmds, cmd("commit-push", func() { a.commitAndPush(t, upstream, pushed) }))
		}
	} else if why != "" && t.sel == "changes" {
		// The shortcut says why it does nothing, as the button does.
		cmds = append(cmds, cmd("commit", func() { a.notice = why }))
	}
	if m := t.cv.model; m != nil {
		if es := m.all(secUnstaged); len(es) > 0 {
			cmds = append(cmds, cmd("stage-all", func() { a.stageEntries(t, m, secUnstaged, es) }))
		}
		if es := m.all(secStaged); len(es) > 0 {
			cmds = append(cmds, cmd("unstage-all", func() { a.stageEntries(t, m, secStaged, es) }))
		}
	}
	upstream := t.s.State.Status != nil && t.s.State.Status.Branch.Upstream != "" && !t.s.State.Status.Branch.Gone
	if !head.Detached && !head.Unborn && upstream {
		// As the sync button pulls: the way git's config says.
		cmds = append(cmds, cmd("pull", func() {
			op, err := write.Pull(w, tabPullMode(t, head.Target), t.s.LocalBranches(), t.s.Seen())
			a.submit(t, op, err)
		}))
		for _, p := range []struct {
			id   string
			mode write.PullMode
		}{{"pull-merge", write.PullMerge}, {"pull-rebase", write.PullRebase}, {"pull-ff", write.PullFFOnly}} {
			cmds = append(cmds, cmd(p.id, func() {
				op, err := write.Pull(w, p.mode, t.s.LocalBranches(), t.s.Seen())
				a.submit(t, op, err)
			}))
		}
	}
	if opt, ok := pushOptions(t); ok && !head.Detached && !head.Unborn {
		opt.Branch = head.Target
		cmds = append(cmds, cmd("push", func() { a.submit(t, write.Push(w, opt), nil) }))
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
	cmds := match(a.commands(p.query), p.query)
	// The options' values are the labels, made unique: two branches
	// whose names differ only in what redact.Control takes out read
	// alike, and the value says which command was chosen.
	values := make([]string, len(cmds))
	seen := map[string]int{}
	for i, cmd := range cmds {
		values[i] = cmd.label
		if n := seen[cmd.label]; n > 0 {
			values[i] += "\x00" + strconv.Itoa(n)
		}
		seen[cmd.label]++
	}
	var run func()
	a.modal(c, &p.open, func() {
		cb := ui.ComboboxBase(c, &p.query)
		cb.Input.AutoFocus().Placeholder(locales.PalettePlaceholder()).Label(locales.PaletteLabel()).Width(520)
		cb.Popup(func(panel ui.Element) {
			km := a.keys()
			for i, cmd := range cmds {
				if cmd.hidden {
					continue
				}
				cb.Item(values[i]).Padding(spaceS, spaceL).Gap(spaceXL).Children(func() {
					ui.Text(c, cmd.label).SingleLine().Grow(1)
					if ks := km.byID[cmd.id]; cmd.id != "" && len(ks) > 0 {
						ui.Text(c, ks[0].label()).FontSize(textSmall).TextColor(c.Theme().TextMuted)
					}
				})
			}
		})
		if value, ok := cb.Chosen(); ok {
			for i, cmd := range cmds {
				if values[i] == value && !cmd.hidden {
					run = cmd.run
					break
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
