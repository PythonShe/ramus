package ui

import (
	"errors"
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
	"ramus/locales"
)

// repoView shows one open repository: the sidebar, the changes, and the
// detail of what is chosen.
func (a *App) repoView(c *ui.Context, t *tab) {
	th := c.Theme()
	st := t.s.State
	switch {
	case st.Opening:
		ui.Column(c).Fill().Center().Gap(spaceL).Children(func() {
			ui.Spinner(c)
			ui.Text(c, locales.RepoOpening(t.s.Path)).TextColor(th.TextMuted)
		})
		return
	case st.Err != nil:
		ui.Column(c).Fill().Center().Gap(spaceL).Children(func() {
			ui.Text(c, locales.RepoCannotOpen(t.s.Path)).Bold()
			ui.Text(c, errText(st.Err)).TextColor(th.TextMuted).MaxWidth(560)
			if ui.Button(c, locales.CommonClose()).Clicked() {
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
			case "worktrees":
				a.worktreeDashboard(c, t)
				return
			case "review":
				a.reviewView(c, t)
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
	ui.Row(c).Padding(bannerPadY, insetX).Gap(spaceL).AlignItems(ui.Center).Background(th.Surface).Children(func() {
		ui.Text(c, locales.RepoTrustBanner()).Grow(1)
		if ui.Button(c, locales.RepoTrust()).Clicked() {
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
	ui.Row(c).Padding(bannerPadY, insetX).Gap(spaceL).AlignItems(ui.Center).Background(th.Surface).Children(func() {
		ui.Text(c, locales.RepoPanic(p.Value)).TextColor(th.Danger).Grow(1).Selectable()
		if ui.Button(c, locales.RepoCopyDetails()).Clicked() {
			a.clip = p.Value + "\n\n" + p.Stack
		}
		if ui.Button(c, locales.RepoReopen()).Clicked() {
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
	section := func(key string, title func(int) string, refs []string, prefix string) {
		if !a.shown(key) {
			return
		}
		open := t.open[key]
		a.hideMenu(ui.SidebarSection(c, title(len(refs)), &open, func() {
			for i, r := range refs {
				if i == sideLimit {
					ui.Text(c, locales.RepoMore(len(refs)-sideLimit)).Padding(rowPadY, insetX).TextColor(c.Theme().TextMuted)
					break
				}
				label := strings.TrimPrefix(r, prefix)
				it := ui.SidebarItem(c, r, nil, label)
				if r == st.Head.Target {
					it.Bold()
				}
				if slices.Contains(t.prefs.hidden, r) {
					it.TextColor(th.TextMuted).Tooltip(locales.RepoHiddenInHistory())
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
		ui.SidebarItem(c, sideChanges, nil, changesLabel(st))
		item("review", locales.RepoSectionReview())
		item("history", locales.RepoSectionHistory())
		item("reflog", locales.RepoSectionReflog())
		item("refs", locales.RepoSectionRefs())
		item("stats", locales.RepoSectionStats())
		item("log", locales.RepoSectionLog())
		item("snapshots", locales.RepoSectionSnapshots())
		section("branches", locales.RepoSectionBranchesCount, heads, "refs/heads/")
		section("remotes", locales.RepoSectionRemotesCount, remotes, "refs/remotes/")
		section("tags", locales.RepoSectionTagsCount, tags, "refs/tags/")
		if t.cmp.to != nil {
			ui.SidebarItem(c, sideCompare, nil, locales.RepoSectionComparison())
		}
		if st.Status != nil && st.Status.Branch.Stashes > 0 {
			item("stashes", locales.RepoSectionStashesCount(st.Status.Branch.Stashes))
		}
		if len(st.Worktrees) > 1 && a.shown("worktrees") {
			open := t.open["worktrees"]
			a.hideMenu(ui.SidebarSection(c, locales.RepoSectionWorktreesCount(len(st.Worktrees)), &open, func() {
				ui.SidebarItem(c, sideWorktrees, nil, locales.RepoAllWorktrees())
				for i, w := range st.Worktrees {
					it := ui.SidebarItem(c, sideWorktree+w.Path, nil, worktreeLabel(w))
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
			a.partMenu(ui.SidebarSection(c, locales.RepoSectionSubmodulesCount(len(st.Submodules)), &open, func() {
				for _, m := range st.Submodules {
					it := ui.SidebarItem(c, sideSubmodule+m.Path, nil, submoduleLabel(m))
					if !m.Present {
						it.TextColor(th.TextMuted)
					}
					a.submoduleMenu(it, t, m)
				}
			}), "submodules", a.submodulesMenu(t))
			t.open["submodules"] = open
		}
	}).Label(locales.RepoSections())
	if side.Changed() {
		a.chooseSidebar(t)
	}
	side.FillHeight()
}

// The ids of sidebar items the sidebar makes itself; the others are the
// keys of its parts (sidebarParts) and ref names. A worktree's or a
// submodule's id is the prefix and its path.
const (
	sideChanges   = "changes"
	sideCompare   = "compare"
	sideWorktrees = "worktrees"
	sideWorktree  = "worktree:"
	sideSubmodule = "submodule:"
)

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
		return locales.RepoSectionChanges()
	}
	return locales.RepoSectionChangesCount(st.Status.Len())
}

func (a *App) statusBar(c *ui.Context, t *tab) {
	th := c.Theme()
	st := t.s.State
	running := runningWrite(t.s.Log)
	if a.Settings.NoStatusBar && running == nil && st.Operation == "" && a.gitWarning == "" && st.WatchErr == nil && st.RefreshErr == nil {
		return
	}
	ui.Row(c).Padding(bannerPadY, insetX).Gap(spaceXL).Background(th.Surface).Children(func() {
		branch := strings.TrimPrefix(st.Head.Target, "refs/heads/")
		if st.Head.Detached {
			branch = locales.RepoDetached()
		}
		ui.Text(c, branch).FontSize(textSmall)
		if st.Operation != "" {
			ui.Text(c, locales.RepoOperationInProgress(string(st.Operation))).FontSize(textSmall).TextColor(th.Warning)
		}
		if len(st.Fallbacks) > 0 {
			ui.Text(c, locales.RepoThroughGit(strings.Join(st.Fallbacks, ", "))).FontSize(textSmall).TextColor(th.TextMuted)
		}
		if e := running; e != nil {
			ui.Row(c).Gap(spaceM).AlignItems(ui.Center).Children(func() {
				ui.Text(c, locales.RepoRunning(e.Name, e.Progress)).FontSize(textSmall).TextColor(th.Accent).SingleLine().MaxWidth(520)
				if ui.Button(c, locales.CommonCancel()).Label(locales.RepoCancelRunning(e.Name)).Padding(0, spaceS).Clicked() {
					e.Cancel()
				}
			})
		}
		ui.Spacer(c)
		if a.gitWarning != "" {
			ui.Text(c, a.gitWarning).FontSize(textSmall).TextColor(th.Warning).SingleLine()
		}
		if st.WatchErr != nil {
			what := locales.RepoNotWatching(errText(st.WatchErr))
			if errors.Is(st.WatchErr, watch.ErrLimit) {
				what = locales.RepoTooManyFolders()
			}
			ui.Text(c, what).FontSize(textSmall).TextColor(th.Warning).SingleLine().MaxWidth(560)
		}
		if st.RefreshErr != nil {
			ui.Text(c, locales.RepoRefreshFailed(errText(st.RefreshErr))).FontSize(textSmall).TextColor(th.Danger).SingleLine().MaxWidth(480)
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
		reasons = append(reasons, locales.RepoPathNotUTF8())
	}
	if f&pathsafe.DotGit != 0 {
		reasons = append(reasons, locales.RepoPathDotGit())
	}
	if f&pathsafe.DotDot != 0 {
		reasons = append(reasons, locales.RepoPathDotDot())
	}
	if f&pathsafe.LineBreak != 0 {
		reasons = append(reasons, locales.RepoPathLineBreak())
	}
	if f&pathsafe.Absolute != 0 {
		reasons = append(reasons, locales.RepoPathAbsolute())
	}
	return "⚠ " + strconv.Quote(p), locales.RepoPathFlagged(strings.Join(reasons, ", "))
}
