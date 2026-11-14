package ui

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/pathsafe"
	"ramus/internal/git/redact"
	"ramus/internal/git/watch"
	"ramus/internal/session"
)

// repoView shows one open repository: the sidebar, the changes, and the
// detail of what is chosen.
func (a *App) repoView(c *ui.Context, t *tab) {
	th := c.Theme()
	st := t.s.State
	switch {
	case st.Opening:
		ui.Column(c).Fill().Center().Gap(12).Children(func() {
			ui.Spinner(c)
			ui.Text(c, "Opening "+t.s.Path).TextColor(th.TextMuted)
		})
		return
	case st.Err != nil:
		ui.Column(c).Fill().Center().Gap(12).Children(func() {
			ui.Text(c, "Cannot open "+t.s.Path).Bold()
			ui.Text(c, errText(st.Err)).TextColor(th.TextMuted).MaxWidth(560)
			if ui.Button(c, "Close").Clicked() {
				a.Close(a.active)
			}
		})
		return
	}
	a.resolvePending(t)
	if !t.expired && t.s.Queue != nil {
		t.expired = true
		t.s.ExpireSnapshots(time.Duration(a.Settings.WithDefaults().SnapshotDays) * 24 * time.Hour)
	}
	ui.Column(c).Grow(1).Children(func() {
		if t.s.Panic != nil {
			a.panicBanner(c, t)
		}
		if st.Restricted {
			a.trustBanner(c, t)
		}
		a.operationBar(c, t)
		a.forcePushOffer(c, t)
		main := func() {
			// After the sidebar, which may have chosen another view.
			a.showView(t)
			switch t.sel {
			case "log":
				a.logView(c, t)
				return
			case "snapshots":
				a.snapshotsView(c, t)
				return
			case "history":
				a.historyView(c, t)
				return
			case "reflog":
				a.reflogView(c, t)
				return
			case "stats":
				a.statsView(c, t)
				return
			case "refs":
				a.refsView(c, t)
				return
			case "stashes":
				a.stashView(c, t)
				return
			case "file":
				a.fileView(c, t)
				return
			case "compare":
				a.commitCompareView(c, t)
				return
			}
			if strings.HasPrefix(t.sel, "refs/heads/") || strings.HasPrefix(t.sel, "refs/tags/") {
				t.refs.showRef(t.sel)
				a.refsView(c, t)
				return
			}
			ui.Split(c, &t.listW, func() { a.changesPane(c, t) }, func() { a.detail(c, t) }).SplitReset(420).Fill()
		}
		if a.Settings.NoSidebar {
			ui.Column(c).Grow(1).FillWidth().Children(main)
		} else {
			ui.Split(c, &t.sideW, func() { a.sidebar(c, t) }, main).SplitReset(220).Grow(1)
		}
		a.statusBar(c, t)
	})
}

func (a *App) trustBanner(c *ui.Context, t *tab) {
	th := c.Theme()
	ui.Row(c).Padding(8, 12).Gap(12).AlignItems(ui.Center).Background(th.Surface).Children(func() {
		ui.Text(c, "Read-only: this repository's hooks, filters and config commands do not run until you trust it.").Grow(1)
		if ui.Button(c, "Trust").Clicked() {
			if err := t.s.Trust(); err != nil {
				a.notice = errText(err)
			}
		}
	})
}

// panicBanner says the repository's work stopped on a bug, and offers to
// open it again.
func (a *App) panicBanner(c *ui.Context, t *tab) {
	th := c.Theme()
	p := t.s.Panic
	ui.Row(c).Padding(8, 12).Gap(12).AlignItems(ui.Center).Background(th.Surface).Children(func() {
		ui.Text(c, "Something in Ramus failed while working on this repository: "+p.Value).TextColor(th.Danger).Grow(1).Selectable()
		if ui.Button(c, "Copy Details").Clicked() {
			a.clip = p.Value + "\n\n" + p.Stack
		}
		if ui.Button(c, "Reopen").Clicked() {
			a.reopen(t)
		}
	})
}

// reopen closes the repository of tab t and opens it again in its place.
func (a *App) reopen(t *tab) {
	i := slices.Index(a.tabs, t)
	if i < 0 {
		return
	}
	path, sel := t.s.Path, t.sel
	a.Close(i)
	a.Open(path)
	n := len(a.tabs) - 1
	a.tabs[n].sel = sel
	a.Move(n, i)
	a.active = i
}

// sideLimit bounds the refs a sidebar section lists; the branch and tag
// views list them all.
const sideLimit = 500

func (a *App) sidebar(c *ui.Context, t *tab) {
	st := t.s.State
	a.loadRefPrefs(t)
	sr := t.sideRefs.build(t)
	heads, remotes, tags := sr.heads, sr.remotes, sr.tags
	th := c.Theme()
	section := func(key, title string, refs []string, prefix string) {
		if !a.shown(key) {
			return
		}
		open := t.open[key]
		a.hideMenu(ui.SidebarSection(c, fmt.Sprintf("%s (%d)", title, len(refs)), &open, func() {
			for i, r := range refs {
				if i == sideLimit {
					ui.Text(c, fmt.Sprintf("%d more", len(refs)-sideLimit)).Padding(4, 12).TextColor(c.Theme().TextMuted)
					break
				}
				label := strings.TrimPrefix(r, prefix)
				it := ui.SidebarItem(c, r, nil, label)
				if r == st.Head.Target {
					it.Bold()
				}
				if slices.Contains(t.prefs.hidden, r) {
					it.TextColor(th.TextMuted).Tooltip("Hidden in history")
				}
				a.sidebarRefMenu(it, t, r)
			}
		}), key)
		t.open[key] = open
	}
	item := func(key, label string) {
		if a.shown(key) {
			a.hideMenu(ui.SidebarItem(c, key, nil, label), key)
		}
	}
	side := ui.Sidebar(c, &t.sel, func() {
		ui.SidebarItem(c, "changes", nil, changesLabel(st))
		item("history", "History")
		item("reflog", "Reflog")
		item("refs", "Branches and Tags")
		item("stats", "Statistics")
		item("log", "Command Log")
		item("snapshots", "Snapshots")
		section("branches", "Branches", heads, "refs/heads/")
		section("remotes", "Remotes", remotes, "refs/remotes/")
		section("tags", "Tags", tags, "refs/tags/")
		if t.cmp.to != nil {
			ui.SidebarItem(c, "compare", nil, "Comparison")
		}
		if st.Status != nil && st.Status.Branch.Stashes > 0 {
			item("stashes", fmt.Sprintf("Stashes (%d)", st.Status.Branch.Stashes))
		}
		if len(st.Worktrees) > 1 && a.shown("worktrees") {
			open := t.open["worktrees"]
			a.hideMenu(ui.SidebarSection(c, fmt.Sprintf("Worktrees (%d)", len(st.Worktrees)), &open, func() {
				for i, w := range st.Worktrees {
					it := ui.SidebarItem(c, "worktree:"+w.Path, nil, worktreeLabel(w))
					switch {
					case w.Current:
						it.Bold()
					case w.Prunable:
						it.TextColor(th.Warning)
					}
					a.worktreeMenu(it, t, w, i == 0)
				}
			}), "worktrees")
			t.open["worktrees"] = open
		}
		if len(st.Submodules) > 0 && a.shown("submodules") {
			open := t.open["submodules"]
			a.partMenu(ui.SidebarSection(c, fmt.Sprintf("Submodules (%d)", len(st.Submodules)), &open, func() {
				for _, m := range st.Submodules {
					it := ui.SidebarItem(c, "submodule:"+m.Path, nil, submoduleLabel(m))
					if !m.Present {
						it.TextColor(th.TextMuted)
					}
					a.submoduleMenu(it, t, m)
				}
			}), "submodules", a.submodulesMenu(t))
			t.open["submodules"] = open
		}
	}).Label("Repository sections")
	if side.Changed() {
		a.chooseSidebar(t)
	}
	side.FillHeight()
}

// chooseSidebar acts on a sidebar item chosen: a worktree or submodule
// opens in its own tab.
func (a *App) chooseSidebar(t *tab) {
	kind, path, ok := strings.Cut(t.sel, ":")
	if !ok {
		return
	}
	switch kind {
	case "worktree":
		for _, w := range t.s.State.Worktrees {
			switch {
			case w.Path != path || w.Current:
			case w.Prunable:
				a.askPrune(t, w)
			default:
				a.Open(path)
			}
		}
	case "submodule":
		a.chooseSubmodule(t, path)
	}
	t.sel = "changes"
}

func changesLabel(st session.State) string {
	if st.Status == nil || st.Status.Len() == 0 {
		return "Changes"
	}
	return fmt.Sprintf("Changes (%d)", st.Status.Len())
}

func (a *App) statusBar(c *ui.Context, t *tab) {
	th := c.Theme()
	st := t.s.State
	running := runningWrite(t.s.Log)
	if a.Settings.NoStatusBar && running == nil && st.Operation == "" && a.gitWarning == "" && st.WatchErr == nil && st.RefreshErr == nil {
		return
	}
	ui.Row(c).Padding(4, 12).Gap(16).Background(th.Surface).Children(func() {
		branch := strings.TrimPrefix(st.Head.Target, "refs/heads/")
		if st.Head.Detached {
			branch = "detached"
		}
		ui.Text(c, branch).FontSize(12)
		if st.Operation != "" {
			ui.Text(c, string(st.Operation)+" in progress").FontSize(12).TextColor(th.Warning)
		}
		if len(st.Fallbacks) > 0 {
			ui.Text(c, "Through git: "+strings.Join(st.Fallbacks, ", ")).FontSize(12).TextColor(th.TextMuted)
		}
		if e := running; e != nil {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, e.Name+": "+e.Progress).FontSize(12).TextColor(th.Accent).SingleLine().MaxWidth(520)
				if ui.Button(c, "Cancel").Label("Cancel "+e.Name).Padding(0, 6).Clicked() {
					e.Cancel()
				}
			})
		}
		ui.Spacer(c)
		if a.gitWarning != "" {
			ui.Text(c, a.gitWarning).FontSize(12).TextColor(th.Warning).SingleLine()
		}
		if st.WatchErr != nil {
			what := "Not watching for changes: " + errText(st.WatchErr)
			if errors.Is(st.WatchErr, watch.ErrLimit) {
				what = "Too many folders to watch for changes"
			}
			ui.Text(c, what+"; refreshing when the window is focused").FontSize(12).TextColor(th.Warning).SingleLine().MaxWidth(560)
		}
		if st.RefreshErr != nil {
			ui.Text(c, "Refresh failed: "+errText(st.RefreshErr)).FontSize(12).TextColor(th.Danger).SingleLine().MaxWidth(480)
		}
	})
}

// runningWrite is the last write still running that Cancel stops and
// that printed something, nil none: the queue runs one at a time, and a
// quick write that prints nothing would only flash by.
func runningWrite(log []*session.LogEntry) *session.LogEntry {
	for i := len(log) - 1; i >= 0; i-- {
		if log[i].Cancellable() && log[i].Progress != "" {
			return log[i]
		}
	}
	return nil
}

// showPath is how a path from git is shown: as it is, unless it holds
// what could hide or fake another path (line breaks, control characters,
// a .git lookalike, ..), when it is quoted and flagged with why. It is
// never hidden.
func showPath(p string) (shown, why string) {
	f := pathsafe.Check(p)
	valid := utf8.ValidString(p)
	if valid && redact.Control(p) != p {
		f |= pathsafe.LineBreak
	}
	if f == 0 && valid {
		return p, ""
	}
	var reasons []string
	if !valid {
		reasons = append(reasons, `is not UTF-8, its other bytes shown as \xNN`)
	}
	if f&pathsafe.DotGit != 0 {
		reasons = append(reasons, "looks like .git")
	}
	if f&pathsafe.DotDot != 0 {
		reasons = append(reasons, "has . or .. in it")
	}
	if f&pathsafe.LineBreak != 0 {
		reasons = append(reasons, "has a line break or control character")
	}
	if f&pathsafe.Absolute != 0 {
		reasons = append(reasons, "is absolute")
	}
	return "⚠ " + strconv.Quote(p), "This path " + strings.Join(reasons, ", ")
}
