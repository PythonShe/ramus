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
	"ramus/internal/git/repo"
	"ramus/internal/git/watch"
	"ramus/internal/session"
	"ramus/internal/ui/fileicon"
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
			ui.Spinner(c).Label(locales.RepoOpening(t.s.Path))
			ui.Text(c, locales.RepoOpening(t.s.Path)).TextColor(th.TextMuted)
		})
		return
	case st.Err != nil:
		var own *repo.OwnershipError
		ui.Column(c).Fill().Center().Gap(spaceL).Children(func() {
			ui.Text(c, locales.RepoCannotOpen(t.s.Path)).Bold()
			if errors.As(st.Err, &own) && own.Fix != "" {
				ui.Text(c, locales.RepoNotOwned()).TextColor(th.TextMuted).MaxWidth(560)
				ui.Row(c).Gap(spaceM).AlignItems(ui.Center).Children(func() {
					ui.Text(c, own.Fix).Font("monospace").FontSize(textSmall).Selectable().MaxWidth(560)
					if ui.Button(c, locales.RepoCopyCommand()).Clicked() {
						a.clip = own.Fix
					}
				})
			} else {
				ui.Text(c, errText(st.Err)).TextColor(th.TextMuted).MaxWidth(560).Selectable()
			}
			ui.Row(c).Gap(spaceM).Children(func() {
				// A drive mounted again, a folder put back, a command run
				// in a terminal: the same repository may open now.
				if ui.PrimaryButton(c, locales.RepoTryAgain()).Clicked() {
					a.reopen(t)
				}
				if ui.Button(c, locales.CommonClose()).Clicked() {
					a.Close(a.active)
				}
			})
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
		if st.Held {
			a.heldBanner(c, t)
		}
		a.operationBar(c, t)
		a.forcePushOffer(c, t)
		main := func() {
			// After the sidebar, which may have chosen another view.
			a.syncNav(t)
			t.nav.router.View(c, func(r *ui.Route) {
				a.page(c, t)
				r.Title(a.pageTitle(t))
			})
		}
		// The split stays as the sidebar hides, its pane closed: the
		// panes beside it, built in another place, would start over,
		// losing what they measured and where they scrolled.
		hidden := a.Settings.NoSidebar
		if hidden && t.sideOut > 0 {
			// Its divider, at the window's edge, was dragged out.
			t.sideW, t.sideOut, hidden = t.sideOut, 0, false
			a.setSidebar(true)
		}
		size := &t.sideW
		if hidden {
			size = &t.sideOut
		}
		ui.Split(c, size, func() {
			if !hidden {
				a.sidebar(c, t)
			}
		}, main).SplitReset(220).Grow(1)
		a.errorBar(c, t)
		a.warningBar(c, t)
	})
}

// page shows the page of tab t that its sidebar item chooses.
func (a *App) page(c *ui.Context, t *tab) {
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
	case sideLocks:
		a.lfsLocksView(c, t)
		return
	case sidePulls:
		a.pullsView(c, t)
		return
	case "worktrees":
		a.worktreeDashboard(c, t)
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
	if t.cv.mode == modeReview {
		// The commit box under the review, as under the list: what is kept
		// is committed without leaving it.
		ui.Column(c).Fill().Children(func() {
			ui.Column(c).Grow(1).Children(func() { a.reviewView(c, t) })
			// As wide as under the list, not across the window.
			ui.Column(c).FillWidth().MaxWidth(max(t.listW, leastListW)).Children(func() { a.commitBox(c, t) })
		})
		return
	}
	keptSplit(c, &t.listW, &t.listRoom, leastListW, leastPatchW, func() { a.changesPane(c, t) }, func() { a.detail(c, t) }).SplitReset(420).Fill()
}

// bannerLead is a banner's text, which text builds, after g in col: it
// says at a glance what kind of banner it is. It takes the room the
// banner's buttons leave.
func bannerLead(c *ui.Context, g *ui.SVG, col ui.Color, text func()) {
	ui.Row(c).Gap(spaceS).AlignItems(ui.Center).Grow(1).Shrink(1).Children(func() {
		ui.Icon(c, g).FontSize(iconMedium).TextColor(col).Shrink(0)
		text()
	})
}

func (a *App) trustBanner(c *ui.Context, t *tab) {
	th := c.Theme()
	ui.Row(c).Padding(bannerPadY, insetX).Gap(spaceL).AlignItems(ui.Center).Background(th.Surface).Children(func() {
		bannerLead(c, glyphs().warning, th.Warning, func() { ui.Text(c, locales.RepoTrustBanner()).Grow(1) })
		if ui.Button(c, locales.RepoTrust()).Clicked() {
			if err := t.s.Trust(); err != nil {
				a.notice = errText(err)
			}
		}
	})
}

// heldBanner says the tab writes nothing while another program works
// in its worktree, and lets the user take it over.
func (a *App) heldBanner(c *ui.Context, t *tab) {
	th := c.Theme()
	ui.Row(c).Padding(bannerPadY, insetX).Gap(spaceL).AlignItems(ui.Center).Background(th.Surface).Children(func() {
		bannerLead(c, glyphs().warning, th.Warning, func() { ui.Text(c, locales.RepoHeldBanner()).Grow(1) })
		if ui.Button(c, locales.RepoTakeOver()).Clicked() {
			t.s.TakeOver()
		}
	})
}

// panicBanner says the repository's work stopped on a bug, and offers to
// open it again.
func (a *App) panicBanner(c *ui.Context, t *tab) {
	th := c.Theme()
	p := t.s.Panic
	ui.Row(c).Padding(bannerPadY, insetX).Gap(spaceL).AlignItems(ui.Center).Background(th.Surface).Children(func() {
		bannerLead(c, glyphs().error, th.Danger, func() { ui.Text(c, locales.RepoPanic(p.Value)).TextColor(th.Danger).Grow(1).Selectable() })
		if ui.Button(c, locales.RepoCopyDetails()).Clicked() {
			a.clip = p.Value + "\n\n" + p.Stack
		}
		if ui.Button(c, locales.RepoReopen()).Clicked() {
			a.reopen(t)
		}
	})
}

// reopen closes the repository of tab t and opens it again in its place,
// first asking, as closing the tab does, whether to stop the write
// running there.
func (a *App) reopen(t *tab) {
	a.askToDrop(t, func() {
		i := slices.Index(a.tabs, t)
		if i < 0 {
			return
		}
		path, sel, n := t.s.Path, t.sel, len(a.tabs)
		a.Close(i)
		a.Open(path)
		if len(a.tabs) != n {
			return // shown in another window instead
		}
		a.tabs[n-1].sel = sel
		a.Move(n-1, i)
		a.active = i
	})
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
	// A section with no refs is left out until it has one.
	section := func(key string, title func(int) string, refs []string, prefix string) {
		if !a.shown(key) || len(refs) == 0 {
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
				shown := label
				current := r == st.Head.Target && !st.Head.Detached
				if current && prefix == "refs/heads/" && a.refIndex(t) {
					// Where the branch stands with its upstream, where it is
					// listed.
					if i, ok := t.refs.byName[r]; ok {
						b := t.refs.info.Branches[i]
						shown = locales.RepoSideBranch(label, b.Ahead, b.Behind)
					}
				}
				// The item cuts a long name at its end: the pointer
				// resting on it shows it whole.
				it := ui.SidebarItem(c, r, nil, shown).Tooltip(label)
				if current {
					it.Bold()
				}
				if sr.hidden[r] {
					it.TextColor(th.TextMuted).Tooltip(locales.RepoHiddenRef(label))
				}
				a.sidebarRefMenu(it, t, r)
				if it.DoubleClicked() {
					a.sidebarSwitch(t, r)
				}
			}
		}), key)
		t.open[key] = open
	}
	item := func(key, label string) {
		if a.shown(key) {
			a.hideMenu(ui.SidebarItem(c, key, nil, label), key)
		}
	}
	// The sidebar holds what is used now and then; Changes and History,
	// used all day, are in the bar (viewSwitch). It runs from the views of
	// the refs to what else the repository holds, then the tools, under a
	// line between each.
	was := t.sel
	side := ui.Sidebar(c, &t.sel, func() {
		item("refs", locales.RepoSectionRefs())
		if a.hasPulls(t) {
			item(sidePulls, a.pullsSection(t))
		}
		if t.cmp.shown() {
			ui.SidebarItem(c, sideCompare, nil, locales.RepoSectionComparison())
		}
		sideDivider(c)
		section("branches", locales.RepoSectionBranchesCount, heads, "refs/heads/")
		section("remotes", locales.RepoSectionRemotesCount, remotes, "refs/remotes/")
		section("tags", locales.RepoSectionTagsCount, tags, "refs/tags/")
		if st.Status != nil && st.Status.Branch.Stashes > 0 {
			item("stashes", locales.RepoSectionStashesCount(st.Status.Branch.Stashes))
		}
		if len(st.Worktrees) > 1 && a.shown("worktrees") {
			open := t.open["worktrees"]
			a.hideMenu(ui.SidebarSection(c, locales.RepoSectionWorktreesCount(len(st.Worktrees)), &open, func() {
				ui.SidebarItem(c, sideWorktrees, nil, locales.RepoAllWorktrees())
				ui.SidebarItem(c, sideNewWorktree, nil, locales.PaletteNewWorktree())
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
		if slices.ContainsFunc(sideTools, a.shown) {
			sideDivider(c)
			// A few items, always open: nothing to fold away.
			ui.SidebarSection(c, locales.RepoSectionTools(), nil, func() {
				if st.LFS {
					item(sideLocks, locales.RepoSectionLocks())
				}
				item("snapshots", locales.RepoSectionSnapshots())
				item("reflog", locales.RepoSectionReflog())
				item("log", locales.RepoSectionLog())
				item("stats", locales.RepoSectionStats())
			})
		}
	}).Label(locales.RepoSections())
	if side.Changed() {
		a.chooseSidebar(t, was)
	}
	side.FillHeight()
}

// sideTools are the keys of the sidebar's tools, at its end.
var sideTools = []string{sideLocks, "snapshots", "reflog", "log", "stats"}

// sideDivider is the line between the sidebar's groups.
func sideDivider(c *ui.Context) {
	ui.Divider(c).Margin(spaceS, 0)
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
	// sideNewWorktree is an item that acts and leaves the page as it was.
	sideNewWorktree = "newworktree:"
)

// chooseSidebar acts on a sidebar item chosen: a worktree or submodule
// opens in its own tab. was is the page shown before, which New Worktree
// leaves as it was.
func (a *App) chooseSidebar(t *tab, was string) {
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
	case "newworktree":
		a.openNewWorktree(t)
		if !strings.Contains(was, ":") {
			t.sel = was
			return
		}
	}
	t.sel = "changes"
}

func changesLabel(st session.State) string {
	if n := changedFiles(st); n > 0 {
		return locales.RepoSectionChangesCount(n)
	}
	return locales.RepoSectionChanges()
}

// warningBar says what keeps the repository from working as it should:
// git too old, changes not watched, a refresh that failed, an operation
// stopped that the operation bar does not cover. It is not there while
// all is well.
func (a *App) warningBar(c *ui.Context, t *tab) {
	th := c.Theme()
	st := t.s.State
	op := st.Operation == repo.Bisect
	fetch := t.s.FetchStopped()
	if !op && a.gitWarning == "" && st.WatchErr == nil && st.RefreshErr == nil && fetch == nil {
		return
	}
	ui.Row(c).Padding(bannerPadY, insetX).Gap(spaceXL).Background(th.Surface).Children(func() {
		if op {
			ui.Text(c, locales.RepoOperationInProgress(string(st.Operation))).FontSize(textSmall).TextColor(th.Warning)
		}
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
			ui.Row(c).Gap(spaceM).AlignItems(ui.Center).Shrink(1).Children(func() {
				why := errText(st.RefreshErr)
				ui.Text(c, locales.RepoRefreshFailed(oneLine(why))).FontSize(textSmall).TextColor(th.Danger).SingleLine().Shrink(1).MaxWidth(480).Tooltip(why)
				// The folder moved, deleted or unmounted and back: opening
				// it again reads it whole.
				if ui.Button(c, locales.RepoReopen()).Clicked() {
					a.reopen(t)
				}
			})
		}
		if fetch != nil {
			ui.Row(c).Gap(spaceM).AlignItems(ui.Center).Shrink(1).Children(func() {
				why := failure(fetch)
				ui.Text(c, locales.RepoFetchStopped(oneLine(why))).FontSize(textSmall).TextColor(th.Warning).SingleLine().Shrink(1).MaxWidth(560).Tooltip(why)
				if ui.Button(c, locales.RepoResumeFetch()).Clicked() {
					t.s.ResumeFetch()
				}
			})
		}
	})
}

// activity shows the write running, in the tab bar, with what git
// printed last and a button that cancels it.
func (a *App) activity(c *ui.Context, t *tab) {
	e := runningWrite(t.s.Log)
	if e == nil {
		return
	}
	th := c.Theme()
	ui.Row(c).Gap(spaceS).AlignItems(ui.Center).Shrink(1).Children(func() {
		ui.Spinner(c).FontSize(textSmall).Label(e.Name)
		ui.Text(c, locales.RepoRunning(e.Name, e.Progress)).FontSize(textSmall).TextColor(th.TextMuted).SingleLine().MaxWidth(320).Shrink(1)
		if closeButton(c, locales.RepoCancelRunning(e.Name)).Clicked() {
			e.Cancel()
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

// fileIcon shows the icon of the file at path, as a code editor's list
// does, in its own colors: a decoration, which assistive technology does
// not read. On a chosen row, k being chosenInk, it is drawn in the row's
// text colour: its own colours can be the accent's and vanish on it.
func fileIcon(c *ui.Context, path string, k ink) {
	if s := fileicon.For(path); s != nil {
		if k == chosenInk(c.Theme()) {
			ui.Icon(c, s).FontSize(iconLarge).Width(iconLarge).Height(iconLarge).TextColor(k.Muted)
			return
		}
		ui.Image(c, s).Width(iconLarge).Height(iconLarge).Role(ui.RoleNone)
		return
	}
	ui.Box(c).Width(iconLarge)
}

// pathLabel shows path shown, from showPath, in a list's row on one line.
// Cut, it loses its start and keeps its end, the file's name and the
// folders nearest it, and the pointer resting on it shows it whole. A
// path flagged comes after a warning glyph.
func pathLabel(c *ui.Context, shown string) ui.Element {
	if !flaggedShown(shown) {
		return ui.Text(c, shown).SingleLine().TruncateStart().Tooltip(shown)
	}
	// A warning before the quoted path, in the colour the caller gives
	// the text, which the row passes down to both.
	return ui.Row(c).Gap(spaceXS).AlignItems(ui.Center).Shrink(1).Tooltip(shown).Children(func() {
		ui.Icon(c, glyphs().warning).FontSize(iconMedium).Shrink(0)
		ui.Text(c, shown).SingleLine().TruncateStart().Shrink(1)
	})
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
	return strconv.Quote(p), locales.RepoPathFlagged(strings.Join(reasons, ", "))
}

// flaggedShown reports whether shown is a path showPath quoted and
// flagged: a path whose own name starts with a quote is not.
func flaggedShown(shown string) bool {
	if !strings.HasPrefix(shown, `"`) {
		return false
	}
	p, err := strconv.Unquote(shown)
	if err != nil {
		return false
	}
	again, why := showPath(p)
	return why != "" && again == shown
}
