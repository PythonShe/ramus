package ui

import (
	"encoding/hex"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/redact"
	"ramus/internal/git/repo"
	"ramus/internal/library"
	"ramus/internal/session"
	"ramus/locales"
)

// dashState is the worktree dashboard of a tab.
type dashState struct {
	table  tableState
	chosen int
	// boxes are the Compare boxes, by path.
	boxes map[string]*bool
	// sums are the summaries read, by path, for the worktrees in readFor;
	// stop ends the reading.
	sums    map[string]session.WorktreeSummary
	readFor []repo.Worktree
	stop    func()
}

// read summarizes the worktrees again when the list changed.
func (d *dashState) read(t *tab) {
	wts := t.s.State.Worktrees
	if d.sums != nil && slices.Equal(d.readFor, wts) {
		return
	}
	if d.stop != nil {
		d.stop()
	}
	d.readFor, d.sums = wts, map[string]session.WorktreeSummary{}
	sums := d.sums
	d.stop = t.s.SummarizeWorktrees(wts, func(s session.WorktreeSummary) { sums[s.Path] = s })
}

// Reload reads the dashboard again, as Cmd+R does.
func (d *dashState) reload() { d.sums = nil }

func (a *App) worktreeDashboard(c *ui.Context, t *tab) {
	d := &t.dash
	d.read(t)
	wts := t.s.State.Worktrees
	branches := map[string]session.BranchInfo{}
	if info := t.refs.info; info != nil {
		for _, b := range info.Branches {
			branches[b.Name] = b
		}
	} else {
		a.loadRefs(t)
	}
	now := a.Now()
	cols := []ui.TableColumn{
		{Title: locales.WorktreeColumnWorktree(), ID: "worktree", MinWidth: 120},
		{Title: locales.WorktreeColumnBranch(), ID: "branch", Width: 200, MinWidth: 60},
		{Title: locales.WorktreeColumnChanges(), ID: "changes", Width: 90, MinWidth: 50, Align: ui.End},
		{Title: locales.WorktreeColumnAheadBehind(), ID: "aheadBehind", Width: 120, MinWidth: 60, Align: ui.End},
		{Title: locales.WorktreeColumnLastActive(), ID: "lastActive", Width: 110, MinWidth: 60, Align: ui.End},
		// The Open button, which stays at the end.
		{ID: "open", Width: 80, Align: ui.End, Fixed: true},
	}
	d.table.Selected = &d.chosen
	d.table.Label = func(i int) string { return worktreeLabel(wts[i]) }
	var act func()
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(barPadY, insetX).Gap(spaceM).AlignItems(ui.Center).Children(func() {
			ui.Text(c, locales.WorktreeCount(len(wts))).Bold().Grow(1)
			picked := d.pickedTwo(wts)
			if ui.Button(c, locales.WorktreeCompareFolders()).Disabled(picked == nil).Clicked() {
				act = func() { a.Compare(picked[0].Path, picked[1].Path, nil) }
			}
			if ui.Button(c, locales.WorktreeCompareBranches()).Disabled(picked == nil || picked[0].Head == "" || picked[1].Head == "").Clicked() {
				act = func() { a.compareWorktrees(t, picked[0], picked[1]) }
			}
		})
		tbl := a.savedTable(c, "worktrees", &d.table, cols, len(wts), func(i, col int) {
			w := wts[i]
			sum, read := d.sums[w.Path]
			k := d.table.ink(c, i)
			switch col {
			case 0:
				ui.Row(c).Gap(spaceM).AlignItems(ui.Center).Children(func() {
					ui.Checkbox(c, d.pickedRef(w.Path), "").Label(locales.WorktreeCompareBox(filepath.Base(w.Path)))
					ui.Column(c).Grow(1).Children(func() {
						name := ui.Text(c, filepath.Base(w.Path)).SingleLine()
						if w.Current {
							name.Bold()
						}
						sub := ui.Text(c, w.Path).FontSize(textCaption).TextColor(k.Muted).SingleLine()
						switch {
						case w.Prunable:
							sub.TextColor(k.Warning)
						case read && sum.Err != nil:
							ui.Text(c, errText(sum.Err)).FontSize(textCaption).TextColor(k.Danger).SingleLine().Tooltip(errText(sum.Err))
						}
					})
				})
			case 1:
				switch {
				case w.Prunable:
					ui.Text(c, locales.WorktreeFolderMissing()).TextColor(k.Warning)
				case w.Branch != "":
					ui.Text(c, redact.Control(strings.TrimPrefix(w.Branch, "refs/heads/"))).SingleLine()
				case w.Head != "":
					ui.Text(c, locales.WorktreeDetachedAt(w.Head[:min(10, len(w.Head))])).TextColor(k.Muted).SingleLine()
				}
			case 2:
				switch {
				case w.Prunable || w.Bare:
				case !read:
					ui.Spinner(c).Size(12, 12)
				case sum.Err == nil:
					ui.Text(c, strconv.Itoa(sum.Changes))
				}
			case 3:
				if b, ok := branches[w.Branch]; ok && b.Upstream != "" && !b.Gone {
					ui.Text(c, fmt.Sprintf("↑%d ↓%d", b.Ahead, b.Behind)).Description(locales.WorktreeAheadBehind(b.Ahead, b.Behind, strings.TrimPrefix(b.Upstream, "refs/remotes/")))
				}
			case 4:
				active := sum.Active
				if b, ok := branches[w.Branch]; ok && time.Unix(b.Time, 0).After(active) {
					active = time.Unix(b.Time, 0)
				}
				if !active.IsZero() {
					ui.Text(c, ago(now, active)).TextColor(k.Muted)
				}
			case 5:
				if !w.Current && ui.Button(c, locales.WorktreeOpen()).Label(locales.WorktreeOpenLabel(filepath.Base(w.Path))).Padding(0, spaceM).Clicked() {
					act = func() { a.openWorktree(t, w) }
				}
			}
		}).Label(locales.WorktreeTableLabel()).Grow(1)
		if tbl.Submitted() && d.chosen >= 0 && d.chosen < len(wts) {
			w := wts[d.chosen]
			act = func() { a.openWorktree(t, w) }
		}
	})
	if act != nil {
		act()
	}
}

// openWorktree opens a worktree in its tab, or offers to prune one whose
// folder is gone. A worktree another program works in, as a coding
// agent, opens view-only (HoldWrites), so that nothing the app does
// there races it; one already open stays as it is.
func (a *App) openWorktree(t *tab, w repo.Worktree) {
	switch {
	case w.Current:
		t.sel = "changes"
	case w.Prunable:
		a.askPrune(t, w)
	case a.Has(w.Path):
		a.Open(w.Path)
	default:
		// The live panel knows; anything else may be minutes old.
		if sum, ok := t.wtp.sums[w.Path]; ok && t.wtp.stop != nil {
			a.openHeld(w.Path, sum.InUse || sum.Err != nil)
			return
		}
		path := w.Path
		t.s.SummarizeWorktrees([]repo.Worktree{w}, func(sum session.WorktreeSummary) {
			a.openHeld(path, sum.InUse || sum.Err != nil)
		})
	}
}

// openHeld opens the repository at path in a tab of its own, holding its
// writes when hold is set and the tab is new.
func (a *App) openHeld(path string, hold bool) {
	known := a.Has(path)
	a.Open(path)
	if !hold || known {
		return
	}
	path = library.Clean(path)
	for _, t := range a.tabs {
		if t.s.Path == path {
			t.s.HoldWrites()
		}
	}
}

// pickedRef is the Compare box of the worktree at path.
func (d *dashState) pickedRef(path string) *bool {
	if d.boxes == nil {
		d.boxes = map[string]*bool{}
	}
	b, ok := d.boxes[path]
	if !ok {
		b = new(bool)
		d.boxes[path] = b
	}
	return b
}

// pickedTwo is the two worktrees picked to compare, nil unless exactly
// two are.
func (d *dashState) pickedTwo(wts []repo.Worktree) []repo.Worktree {
	var out []repo.Worktree
	for _, w := range wts {
		if b, ok := d.boxes[w.Path]; ok && *b && !w.Prunable {
			out = append(out, w)
		}
	}
	if len(out) != 2 {
		return nil
	}
	return out
}

// compareWorktrees compares what the second worktree's HEAD has with
// the first's.
func (a *App) compareWorktrees(t *tab, x, y repo.Worktree) {
	from, err1 := hex.DecodeString(x.Head)
	to, err2 := hex.DecodeString(y.Head)
	if err1 != nil || err2 != nil {
		return
	}
	a.compareCommits(t, from, to, worktreeLabel(x), worktreeLabel(y))
}
