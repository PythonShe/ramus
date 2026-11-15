package ui

import (
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/redact"
	"ramus/internal/git/repo"
	"ramus/internal/session"
	"ramus/locales"
)

// wtPanel is the worktree panel of a tab: the button beside the branch
// picker, and below it, live, what changed in each worktree against its
// HEAD, for watching what agents working there do. It only reads.
type wtPanel struct {
	open bool
	list ui.ListState
	// sums are the latest summaries by path of the worktrees watching
	// lists; stop ends the watch. A new list keeps those it still has,
	// so nothing blinks out as a worktree is added.
	sums     map[string]session.WorktreeSummary
	watching []repo.Worktree
	stop     func()
	expanded map[string]bool
	// file is the file whose patch shows in place of the list, nil none.
	file      *wtFile
	patch     session.PatchResult
	pv        patchView
	stopPatch func()
}

// wtFile is a file changed in a worktree, as the panel shows its patch.
type wtFile struct {
	worktree, name string
	f              session.WorktreeFile
}

// wtRow is a row of the panel: a worktree, or one of its files.
type wtRow struct {
	w    int
	file int // -1 for the worktree itself
}

const (
	wtPanelWidth  = 480
	wtPanelRows   = 14 // shown before the list scrolls
	wtPatchHeight = 440
)

// watch reads the worktrees again and again while the panel is open,
// starting over as the list of them changes.
func (p *wtPanel) watch(a *App, t *tab) {
	wts := t.s.State.Worktrees
	if p.stop != nil && slices.Equal(p.watching, wts) {
		return
	}
	if p.stop != nil {
		p.stop()
	}
	if p.sums == nil {
		p.sums = map[string]session.WorktreeSummary{}
	}
	for path := range p.sums {
		if !slices.ContainsFunc(wts, func(w repo.Worktree) bool { return w.Path == path }) {
			delete(p.sums, path)
		}
	}
	p.watching = wts
	p.stop = t.s.WatchWorktrees(wts, func(sum session.WorktreeSummary) {
		p.sums[sum.Path] = sum
		p.follow(a, t, sum)
	})
}

// follow reads the patch shown again when its file changed.
func (p *wtPanel) follow(a *App, t *tab, sum session.WorktreeSummary) {
	f := p.file
	if f == nil || f.worktree != sum.Path {
		return
	}
	for _, g := range sum.Files {
		if g.Path == f.f.Path {
			if !g.Changed.Equal(f.f.Changed) || g.Added != f.f.Added || g.Removed != f.f.Removed {
				f.f = g
				p.readPatch(a, t)
			}
			return
		}
	}
}

// halt stops reading: the panel closed, or its tab is not shown.
func (p *wtPanel) halt() {
	if p.stop != nil {
		p.stop()
		p.stop, p.watching = nil, nil
	}
	if p.stopPatch != nil {
		p.stopPatch()
		p.stopPatch = nil
	}
	p.file = nil
}

// show shows the patch of a file in place of the list.
func (p *wtPanel) show(a *App, t *tab, w repo.Worktree, f session.WorktreeFile) {
	p.file = &wtFile{worktree: w.Path, name: filepath.Base(w.Path), f: f}
	p.patch, p.pv = session.PatchResult{}, patchView{}
	p.readPatch(a, t)
}

func (p *wtPanel) readPatch(a *App, t *tab) {
	if p.stopPatch != nil {
		p.stopPatch()
	}
	f := p.file
	p.stopPatch = t.s.WorktreePatch(f.worktree, f.f.Path, f.f.Untracked, func(res session.PatchResult) {
		if p.file == f {
			p.patch = res
			a.setPatch(&p.pv, res)
		}
	})
}

// keepPanel stops the panel of tab t reading once it is closed or its
// tab is not the one shown.
func (a *App) keepPanel(t *tab) {
	p := &t.wtp
	if p.stop != nil && (!p.open || a.current() != t || len(t.s.State.Worktrees) < 2) {
		p.open = false
		p.halt()
	}
}

// worktreeButton opens the worktree panel, beside the branch picker,
// when the repository has more than one worktree.
func (a *App) worktreeButton(c *ui.Context, t *tab) {
	p := &t.wtp
	if len(t.s.State.Worktrees) < 2 {
		return
	}
	label := locales.WorktreePanel()
	var b ui.Element
	ui.Toolbar(c, func() {
		b = ui.Button(c, "").Label(label).Tooltip(label)
		b.Children(func() { ui.Icon(c, wtIcons().stack).FontSize(16) })
		if b.Clicked() {
			p.open = !p.open
		}
	}).Label(label)
	if !p.open {
		return
	}
	p.watch(a, t)
	var act func()
	ui.Popover(c, b, &p.open, func() {
		ui.Column(c).Width(wtPanelWidth).Gap(spaceS).Children(func() {
			if p.file != nil {
				a.wtPatchView(c, p)
				return
			}
			act = a.wtList(c, t, p)
		})
	})
	if act != nil {
		act()
	}
}

// wtList is the panel's list: each worktree with what changed there,
// and the files of those expanded. It returns what was clicked, to run
// once the panel is built.
func (a *App) wtList(c *ui.Context, t *tab, p *wtPanel) (act func()) {
	wts := t.s.State.Worktrees
	var rows []wtRow
	for i, w := range wts {
		if w.Bare {
			continue
		}
		rows = append(rows, wtRow{i, -1})
		if p.expanded[w.Path] {
			for j := range p.sums[w.Path].Files {
				rows = append(rows, wtRow{i, j})
			}
		}
	}
	now := a.Now()
	ui.List(c, &p.list, len(rows), func(i int) {
		r := rows[i]
		w := wts[r.w]
		sum, read := p.sums[w.Path]
		if r.file >= 0 {
			f := sum.Files[r.file]
			if wtFileRow(c, f).Clicked() {
				act = func() { p.show(a, t, w, f) }
			}
			return
		}
		row, open := wtWorktreeRow(c, w, sum, read, p.expanded[w.Path], now)
		switch {
		case open:
			act = func() { a.openWorktree(t, w) }
		case row.Clicked():
			act = func() {
				if p.expanded == nil {
					p.expanded = map[string]bool{}
				}
				p.expanded[w.Path] = !p.expanded[w.Path]
			}
		}
	}).Label(locales.WorktreePanelList()).FillWidth().Height(float32(min(len(rows), wtPanelRows) * rowHeight))
	return act
}

// wtWorktreeRow is a worktree in the panel: its name and branch, how
// many files and lines changed there, and when; open reports its Open
// button clicked.
func wtWorktreeRow(c *ui.Context, w repo.Worktree, sum session.WorktreeSummary, read, expanded bool, now time.Time) (row ui.Element, open bool) {
	th := c.Theme()
	name := filepath.Base(w.Path)
	row = ui.Row(c).Height(rowHeight).Padding(0, spaceS).Gap(spaceS).AlignItems(ui.Center).Radius(6).
		Role(ui.RoleButton).Cursor(ui.CursorPointer).Label(locales.WorktreePanelShowFiles(name))
	if row.Hovered() {
		row.Background(th.SurfaceHover)
	}
	row.Children(func() {
		chevron := wtIcons().right
		if expanded {
			chevron = pickIcons().chevron
		}
		ui.Icon(c, chevron).FontSize(10).TextColor(th.TextMuted).Width(12)
		n := ui.Text(c, name).SingleLine().TruncateMiddle().Shrink(1)
		if w.Current {
			n.Bold()
		}
		branch := ""
		switch {
		case w.Branch != "":
			branch = redact.Control(strings.TrimPrefix(w.Branch, "refs/heads/"))
		case w.Head != "":
			branch = locales.WorktreeDetachedAt(w.Head[:min(10, len(w.Head))])
		}
		ui.Text(c, branch).FontSize(textSmall).TextColor(th.TextMuted).SingleLine().TruncateMiddle().Shrink(2)
		ui.Spacer(c)
		switch {
		case w.Prunable:
			ui.Text(c, locales.WorktreeFolderMissing()).FontSize(textSmall).TextColor(th.Warning)
		case !read:
			ui.Spinner(c).Size(12, 12)
		case sum.Err != nil:
			ui.Text(c, errText(sum.Err)).FontSize(textSmall).TextColor(th.Danger).SingleLine().MaxWidth(160).Tooltip(errText(sum.Err))
		default:
			if sum.Changes > 0 {
				ui.Text(c, locales.WorktreePanelFiles(sum.Changes)).FontSize(textSmall).TextColor(th.TextMuted)
				lineCounts(c, sum.Added, sum.Removed)
			}
			when := ago(now, sum.Active)
			col := th.TextMuted
			if sum.Busy {
				when, col = locales.WorktreePanelChangingNow(), th.Accent
			}
			ui.Text(c, when).FontSize(textSmall).TextColor(col).SingleLine()
		}
		if !w.Current && !w.Prunable {
			// In a toolbar: no face until the pointer is on it.
			ui.Toolbar(c, func() {
				b := ui.Button(c, "").Label(locales.WorktreePanelOpenLabel(name)).Tooltip(locales.WorktreePanelOpenInTab()).Padding(0, spaceXS)
				b.Children(func() { ui.Icon(c, wtIcons().open).FontSize(13).TextColor(th.TextMuted) })
				open = b.Clicked()
			}).Label(locales.WorktreePanelOpenLabel(name))
		}
	})
	return row, open
}

// wtFileRow is a file changed in a worktree, under it in the panel.
func wtFileRow(c *ui.Context, f session.WorktreeFile) ui.Element {
	th := c.Theme()
	shown, _ := showPath(f.Path)
	row := ui.Row(c).Height(rowHeight).Padding(0, spaceS, 0, spaceS+12+spaceS).Gap(spaceS).AlignItems(ui.Center).Radius(6).
		Role(ui.RoleButton).Cursor(ui.CursorPointer).Label(shown)
	if row.Hovered() {
		row.Background(th.SurfaceHover)
	}
	st := byte('M')
	switch {
	case f.Untracked:
		st = 'A'
	case f.Deleted:
		st = 'D'
	}
	row.Children(func() {
		ui.Column(c).Width(6).Height(6).Radius(3).Background(fileStatusColor(th, st)).Tooltip(fileStatusWord(st))
		pathLabel(c, shown).Shrink(1)
		ui.Spacer(c)
		if f.Binary {
			ui.Text(c, locales.PatchBinary()).FontSize(textSmall).TextColor(th.TextMuted)
		} else {
			lineCounts(c, f.Added, f.Removed)
		}
	})
	return row
}

// wtPatchView shows the patch of the file chosen in the panel, read only,
// with the way back to the list.
func (a *App) wtPatchView(c *ui.Context, p *wtPanel) {
	th := c.Theme()
	f := p.file
	ui.Row(c).Gap(spaceS).AlignItems(ui.Center).Children(func() {
		back := false
		ui.Toolbar(c, func() {
			b := ui.Button(c, "").Label(locales.WorktreePanelBack()).Tooltip(locales.WorktreePanelBack()).Padding(0, spaceXS)
			b.Children(func() { ui.Icon(c, wtIcons().left).FontSize(12).TextColor(th.TextMuted) })
			back = b.Clicked()
		}).Label(locales.WorktreePanelBack())
		if back {
			if p.stopPatch != nil {
				p.stopPatch()
				p.stopPatch = nil
			}
			p.file = nil
		}
		shown, _ := showPath(f.f.Path)
		pathLabel(c, shown).Shrink(1)
		ui.Spacer(c)
		ui.Text(c, f.name).FontSize(textSmall).TextColor(th.TextMuted).SingleLine().TruncateMiddle().Shrink(2)
	})
	ui.Column(c).FillWidth().Height(wtPatchHeight).Children(func() {
		a.patchBody(c, nil, &p.pv, p.patch, nil)
	})
}

// wtIcons are the worktree panel's icons, parsed once.
var wtIcons = sync.OnceValue(func() (icons struct{ stack, right, left, open *ui.SVG }) {
	// Worktrees: three folders' tops, one behind the other.
	icons.stack = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linejoin="round" stroke-linecap="round"><rect x="3" y="9" width="18" height="11" rx="2.5"/><path d="M5.5 6h13M8 3h8"/></svg>`))
	icons.right = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M9.5 6l6 6-6 6"/></svg>`))
	icons.left = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M14.5 6l-6 6 6 6"/></svg>`))
	// Open in a tab: a box with an arrow leaving it.
	icons.open = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round"><path d="M13 5H7a2 2 0 0 0-2 2v10a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2v-6"/><path d="M15 4h5v5M20 4l-9 9"/></svg>`))
	return icons
})
