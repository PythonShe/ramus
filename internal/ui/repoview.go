package ui

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/pathsafe"
	"ramus/internal/git/redact"
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
		if st.Restricted {
			a.trustBanner(c, t)
		}
		a.operationBar(c, t)
		ui.Split(c, &t.sideW, func() { a.sidebar(c, t) }, func() {
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
		}).SplitReset(220).Grow(1)
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
		open := t.open[key]
		ui.SidebarSection(c, fmt.Sprintf("%s (%d)", title, len(refs)), &open, func() {
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
		})
		t.open[key] = open
	}
	side := ui.Sidebar(c, &t.sel, func() {
		ui.SidebarItem(c, "changes", nil, changesLabel(st))
		ui.SidebarItem(c, "history", nil, "History")
		ui.SidebarItem(c, "reflog", nil, "Reflog")
		ui.SidebarItem(c, "refs", nil, "Branches and Tags")
		ui.SidebarItem(c, "stats", nil, "Statistics")
		ui.SidebarItem(c, "log", nil, "Command Log")
		ui.SidebarItem(c, "snapshots", nil, "Snapshots")
		section("branches", "Branches", heads, "refs/heads/")
		section("remotes", "Remotes", remotes, "refs/remotes/")
		section("tags", "Tags", tags, "refs/tags/")
		if t.cmp.to != nil {
			ui.SidebarItem(c, "compare", nil, "Comparison")
		}
		if st.Status != nil && st.Status.Branch.Stashes > 0 {
			ui.SidebarItem(c, "stashes", nil, fmt.Sprintf("Stashes (%d)", st.Status.Branch.Stashes))
		}
		if len(st.Worktrees) > 1 {
			open := t.open["worktrees"]
			ui.SidebarSection(c, fmt.Sprintf("Worktrees (%d)", len(st.Worktrees)), &open, func() {
				for _, w := range st.Worktrees {
					label := filepath.Base(w.Path)
					switch {
					case w.Branch != "":
						label += "  " + strings.TrimPrefix(w.Branch, "refs/heads/")
					case w.Detached:
						label += "  detached"
					}
					it := ui.SidebarItem(c, "worktree:"+w.Path, nil, label)
					if w.Current {
						it.Bold()
					}
				}
			})
			t.open["worktrees"] = open
		}
		if len(st.Submodules) > 0 {
			open := t.open["submodules"]
			ui.SidebarSection(c, fmt.Sprintf("Submodules (%d)", len(st.Submodules)), &open, func() {
				for _, m := range st.Submodules {
					ui.SidebarItem(c, "submodule:"+m.Path, nil, m.Path)
				}
			})
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
		if path != t.s.Path {
			a.Open(path)
		}
	case "submodule":
		a.Open(filepath.Join(t.s.Path, filepath.FromSlash(path)))
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
		ui.Spacer(c)
		if a.gitWarning != "" {
			ui.Text(c, a.gitWarning).FontSize(12).TextColor(th.Warning).SingleLine()
		}
		if st.WatchErr != nil {
			// Changes then show when the window gains focus, not as they
			// happen.
			ui.Text(c, "Not watching for changes: "+errText(st.WatchErr)).FontSize(12).TextColor(th.Warning).SingleLine().MaxWidth(480)
		}
		if st.RefreshErr != nil {
			ui.Text(c, "Refresh failed: "+errText(st.RefreshErr)).FontSize(12).TextColor(th.Danger).SingleLine().MaxWidth(480)
		}
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
