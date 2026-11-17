package ui

import (
	"context"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/refs"
	"ramus/internal/session"
	"ramus/locales"
)

// branchPick is the branch picker of a tab: the button in the tab bar
// naming the branch checked out, and the panel below it that switches.
type branchPick struct {
	open  bool
	query string
	// hi is the row the arrows and the pointer are on, which Enter
	// chooses.
	hi int
	// hover is the row under the pointer, -1 none: the pointer moving
	// onto a row takes hi there, but resting on one leaves hi to the
	// arrows.
	hover int
	list  ui.ListState
	// fetchErr is why the fetch the picker started failed, "" when it
	// did not.
	fetchErr string
	// rows are those matching query, rebuilt as it or the refs change.
	rows []pickRow
	// rowsFor is the refs they were built from, and their pins.
	rowsFor  *refs.List
	rowsGen  int
	rowsWith string
	rowsInfo *session.RefInfo
	// info indexes the local branches of infoFor by full name, for the
	// state each row shows.
	info    map[string]session.BranchInfo
	infoFor *session.RefInfo
}

// pickRow is a branch in the picker, or the heading of the remote ones.
type pickRow struct {
	ref     string
	heading string
}

const (
	// pickFresh is how long a fetch keeps the picker from starting
	// another.
	pickFresh = time.Minute
	pickWidth = 400
	pickRows  = 10 // shown before the list scrolls
)

// branchButton names the branch checked out and opens the picker.
func (a *App) branchButton(c *ui.Context, t *tab) {
	th := c.Theme()
	head := t.s.State.Head
	name := strings.TrimPrefix(head.Target, "refs/heads/")
	if head.Detached {
		name = locales.BranchPickerDetached()
	}
	p := &t.pick
	label := locales.BranchPickerLabel(name)
	var b ui.Element
	ui.Toolbar(c, func() {
		b = ui.Button(c, "").Label(label).Tooltip(locales.BranchPickerTooltip())
		b.Children(func() {
			ui.Row(c).Gap(spaceS).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, pickIcons().branch).FontSize(iconMedium).TextColor(th.TextMuted)
				ui.Text(c, name).SingleLine().TruncateMiddle().MaxWidth(220)
				ui.Icon(c, pickIcons().chevron).FontSize(iconSmall).TextColor(th.TextMuted)
			})
		})
		if b.Clicked() {
			if p.open {
				p.open = false
			} else {
				a.openPicker(t)
			}
		}
	}).Label(label)
	if !p.open {
		return
	}
	var chosen *pickRow
	var act func()
	newBranch := false
	ui.Popover(c, b, &p.open, func() {
		ui.Column(c).Width(pickWidth).Gap(spaceS).Children(func() {
			var f ui.Element
			ui.Row(c).Gap(spaceS).AlignItems(ui.Center).Children(func() {
				f = ui.SearchField(c, &p.query).Label(locales.BranchPickerSearch()).Placeholder(locales.BranchPickerSearch()).AutoFocus().Grow(1)
				if t.s.Fetching() {
					ui.Spinner(c).FontSize(textSmall).Label(locales.BranchPickerFetching())
				}
			})
			rows := p.build(t)
			move := func(d int) {
				for i := p.hi + d; i >= 0 && i < len(rows); i += d {
					if rows[i].heading == "" {
						p.hi = i
						p.list.ScrollIntoView(i)
						return
					}
				}
			}
			switch {
			case f.Shortcut(0, ui.KeyDown):
				move(1)
			case f.Shortcut(0, ui.KeyUp):
				move(-1)
			}
			canCreate := !t.s.State.Head.Unborn && len(t.s.State.Head.OID) > 0
			switch {
			case f.Submitted() && p.hi >= 0 && p.hi < len(rows):
				chosen = &rows[p.hi]
			case f.Submitted() && len(rows) == 0 && strings.TrimSpace(p.query) != "" && canCreate:
				// A name no branch has: make it, as New Branch would.
				newBranch = true
			}
			if len(rows) == 0 {
				if q := strings.TrimSpace(p.query); q != "" && canCreate {
					// What Enter does with a name no branch has, in view.
					nb := ui.ButtonBase(c).Padding(rowPadY, spaceS).Radius(th.Radius).FillWidth().Justify(ui.Start).Label(locales.BranchPickerCreate(q))
					if nb.Hovered() {
						nb.Background(th.SurfaceHover)
					}
					nb.Children(func() { ui.Text(c, locales.BranchPickerCreate(q)).SingleLine().TruncateMiddle() })
					if nb.Clicked() {
						newBranch = true
					}
				} else {
					ui.Text(c, locales.BranchPickerNone()).FontSize(textSmall).TextColor(th.TextMuted).Padding(spaceM, spaceS)
				}
			} else {
				hover := -1
				ui.List(c, &p.list, len(rows), func(i int) {
					r := pickRowView(c, t, p, rows[i], i == p.hi)
					if rows[i].heading != "" {
						return
					}
					ref := rows[i].ref
					r.ContextMenu(func(m *ui.Menu) {
						var pick func()
						if strings.HasPrefix(ref, "refs/remotes/") {
							a.remoteBranchMenu(m, t, ref, &pick)
						} else if b, ok := p.info[ref]; ok {
							a.branchMenu(m, t, b, &pick)
						}
						if pick != nil {
							act = pick
						}
					})
					if r.Clicked() {
						chosen = &rows[i]
					}
					if r.Hovered() {
						hover = i
					}
				}).Label(locales.BranchPickerList()).FillWidth().Height(float32(min(len(rows), pickRows) * rowHeight))
				if hover != p.hover {
					p.hover = hover
					if hover >= 0 && hover != p.hi {
						// The rows are drawn: draw them again with the
						// highlight under the pointer, or it shows a move late.
						p.hi = hover
						c.AnimationFrame()
					}
				}
			}
			if p.fetchErr != "" {
				ui.Row(c).Gap(spaceM).AlignItems(ui.Center).Padding(0, spaceS).Children(func() {
					ui.Text(c, p.fetchErr).FontSize(textSmall).TextColor(th.Danger).Grow(1).Shrink(1)
					if ui.Button(c, locales.BranchPickerRetry()).FontSize(textSmall).Clicked() {
						a.fetchForPicker(t)
					}
				})
			}
			if canCreate {
				ui.Divider(c)
				// Its text starts where the branches' names do, past the check.
				nb := ui.ButtonBase(c).Padding(rowPadY, spaceS, rowPadY, spaceS+12+spaceS).Radius(th.Radius).FillWidth().Justify(ui.Start)
				if nb.Hovered() {
					nb.Background(th.SurfaceHover)
				}
				nb.Children(func() { ui.Text(c, locales.KeysNewBranch()) })
				if nb.Clicked() {
					newBranch = true
				}
			}
		})
	})
	if act != nil {
		p.open = false
		act()
		return
	}
	switch {
	case chosen != nil:
		p.open = false
		a.switchTo(t, chosen.ref)
	case newBranch:
		p.open = false
		a.branchUI = branchDialog{open: true, t: t, commit: hex.EncodeToString(t.s.State.Head.OID), name: strings.TrimSpace(p.query), checkout: true}
	}
}

// openPicker opens the branch picker of tab t, empty of a query.
func (a *App) openPicker(t *tab) {
	p := &t.pick
	p.open, p.query, p.hi, p.hover, p.rowsFor, p.fetchErr = true, "", -1, -1, nil, ""
	a.loadRefs(t)
	// The remote branches are as the last fetch left them: fetch, unless
	// one just ran or the user stopped this repository fetching.
	a.loadRefPrefs(t)
	if !t.prefs.fetchOff {
		a.fetchFor(t, pickFresh)
	}
}

// fetchForPicker fetches again for the picker, whatever just ran.
func (a *App) fetchForPicker(t *tab) {
	t.pick.fetchErr = ""
	a.fetchFor(t, 0)
}

func (a *App) fetchFor(t *tab, fresh time.Duration) {
	p := &t.pick
	t.s.FetchFresh(fresh, func(err error) {
		if err != nil && !errors.Is(err, context.Canceled) {
			p.fetchErr = locales.BranchPickerFetchFailed(oneLine(failure(err)))
		}
	})
}

// indexInfo keeps the local branches' state by name, read with the
// branches view's refs.
func (p *branchPick) indexInfo(t *tab) {
	info := t.refs.info
	if info == p.infoFor {
		return
	}
	p.infoFor, p.info = info, nil
	if info == nil {
		return
	}
	p.info = make(map[string]session.BranchInfo, len(info.Branches))
	for _, b := range info.Branches {
		p.info[b.Name] = b
	}
}

// pickRowView builds a row of the picker: a heading, or a branch with a
// check by the one checked out.
func pickRowView(c *ui.Context, t *tab, p *branchPick, r pickRow, hi bool) ui.Element {
	th := c.Theme()
	if r.heading != "" {
		return ui.Text(c, r.heading).FontSize(textCaption).TextColor(th.TextMuted).Height(rowHeight).Padding(spaceS+2, spaceS, 0, spaceS)
	}
	row := ui.Row(c).Height(rowHeight).Padding(0, spaceS).Gap(spaceS).AlignItems(ui.Center).Radius(th.Radius).
		Role(ui.RoleButton).Cursor(ui.CursorPointer).Label(shortRef(r.ref))
	fg, muted := th.Text, th.TextMuted
	if hi {
		row.Background(th.Accent)
		fg, muted = th.AccentText, th.AccentText
	}
	row.Children(func() {
		check := ""
		if r.ref == t.s.State.Head.Target && !t.s.State.Head.Detached {
			check = "✓"
		}
		ui.Text(c, check).Width(12).TextColor(fg)
		ui.Text(c, shortRef(r.ref)).SingleLine().TruncateMiddle().Shrink(1).Grow(1).TextColor(fg)
		b, ok := p.info[r.ref]
		if !ok {
			return
		}
		if b.Worktree != "" {
			ui.Text(c, locales.RefsInWorktree(filepath.Base(b.Worktree))).FontSize(textSmall).TextColor(muted).SingleLine().Description(locales.RefsCheckedOutIn(b.Worktree))
		}
		if b.Ahead > 0 {
			ui.Text(c, locales.RepoPickerAhead(b.Ahead)).FontSize(textSmall).TextColor(muted).Description(locales.RepoPickerAheadLabel(b.Ahead))
		}
		if b.Behind > 0 {
			ui.Text(c, locales.RepoPickerBehind(b.Behind)).FontSize(textSmall).TextColor(muted).Description(locales.RepoPickerBehindLabel(b.Behind))
		}
	})
	return row
}

// build is the rows matching the query: the local branches, pinned
// first, then the remote ones under their heading.
func (p *branchPick) build(t *tab) []pickRow {
	sr := t.sideRefs.build(t)
	p.indexInfo(t)
	if p.rowsFor == sr.refs && p.rowsFor != nil && p.rowsGen == sr.gen && p.rowsWith == p.query && p.rowsInfo == p.infoFor {
		return p.rows
	}
	// A fetch landing while the picker is open keeps the arrows on the
	// branch they were on.
	keep := ""
	if p.rowsWith == p.query && p.hi >= 0 && p.hi < len(p.rows) {
		keep = p.rows[p.hi].ref
	}
	p.rowsFor, p.rowsGen, p.rowsWith, p.rowsInfo, p.rows = sr.refs, sr.gen, p.query, p.infoFor, nil
	words := strings.Fields(strings.ToLower(p.query))
	matches := func(ref string) bool {
		s := strings.ToLower(shortRef(ref))
		for _, w := range words {
			if !strings.Contains(s, w) {
				return false
			}
		}
		return true
	}
	for _, r := range sr.heads {
		if matches(r) {
			p.rows = append(p.rows, pickRow{ref: r})
		}
	}
	heading := false
	local := make(map[string]bool, len(sr.heads))
	for _, r := range sr.heads {
		local[r] = true
	}
	// stands reports whether a local branch shown stands for remote
	// branch r: one of its name that tracks it, or, until the upstreams
	// are read, one of its name. Its menu and the sidebar still reach r.
	stands := func(r string) bool {
		l := "refs/heads/" + localName(r)
		if !local[l] || !matches(l) {
			return false
		}
		if p.info == nil {
			return true
		}
		return p.info[l].Upstream == r
	}
	for _, r := range sr.remotes {
		if !matches(r) || stands(r) {
			continue
		}
		if !heading {
			p.rows = append(p.rows, pickRow{heading: locales.BranchPickerRemote()})
			heading = true
		}
		p.rows = append(p.rows, pickRow{ref: r})
	}
	// The arrows start on the first branch: the current one, when there
	// is no query, is first only if it is pinned, so start past it.
	p.hi = -1
	if keep != "" {
		for i, r := range p.rows {
			if r.ref == keep {
				p.hi = i
				return p.rows
			}
		}
	}
	for i, r := range p.rows {
		// Enter on a fresh picker takes a local branch, never a remote
		// one the user has not looked at.
		remote := strings.HasPrefix(r.ref, "refs/remotes/")
		if r.heading == "" && (len(words) > 0 || !remote && r.ref != t.s.State.Head.Target) {
			p.hi = i
			break
		}
	}
	return p.rows
}

// switchTo checks out ref: a local branch, or a remote one as a local
// branch of its name.
func (a *App) switchTo(t *tab, ref string) {
	if t.s.Queue == nil {
		return
	}
	if strings.HasPrefix(ref, "refs/remotes/") {
		a.checkOutRemote(t, ref)
		return
	}
	if ref == t.s.State.Head.Target && !t.s.State.Head.Detached {
		return
	}
	b := session.BranchInfo{Name: ref}
	if info := t.refs.info; info != nil {
		for _, x := range info.Branches {
			if x.Name == ref {
				b = x
			}
		}
	}
	a.checkOut(t, b)
}

// pickIcons are the branch picker's icons, parsed once.
var pickIcons = sync.OnceValue(func() (icons struct{ branch, chevron *ui.SVG }) {
	// A branch: a line from a commit below to one above, and one off it
	// to the side.
	icons.branch = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round"><circle cx="7" cy="5.5" r="2.25"/><circle cx="7" cy="18.5" r="2.25"/><circle cx="17" cy="7" r="2.25"/><path d="M7 7.75v8.5M17 9.25c0 4.5-10 3-10 7"/></svg>`))
	icons.chevron = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M6 9.5l6 6 6-6"/></svg>`))
	return icons
})
