package ui

import (
	"cmp"
	"context"
	"encoding/hex"
	"errors"
	"path/filepath"
	"slices"
	"strings"
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
	// heads are the local branches in the order listed, for the refs,
	// pins and state of headsFor: sorted once, not as each key is typed.
	heads    []string
	headsFor headsKey
}

type headsKey struct {
	refs *refs.List
	gen  int
	info *session.RefInfo
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
				ui.Icon(c, glyphs().branch).FontSize(iconMedium).TextColor(th.TextMuted)
				ui.Text(c, name).SingleLine().TruncateMiddle().MaxWidth(220)
				ui.Icon(c, glyphs().chevronDown).FontSize(iconSmall).TextColor(th.TextMuted)
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
				now := a.Now()
				hover := -1
				ui.List(c, &p.list, len(rows), func(i int) {
					r := pickRowView(c, t, p, rows[i], i == p.hi, now)
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
				nb := ui.ButtonBase(c).Padding(rowPadY, spaceS, rowPadY, spaceS+markW+spaceS).Radius(th.Radius).FillWidth().Justify(ui.Start)
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
func pickRowView(c *ui.Context, t *tab, p *branchPick, r pickRow, hi bool, now time.Time) ui.Element {
	th := c.Theme()
	if r.heading != "" {
		return ui.Text(c, r.heading).FontSize(textCaption).TextColor(th.TextMuted).Height(rowHeight).Padding(spaceS+2, spaceS, 0, spaceS)
	}
	row := ui.Row(c).Height(rowHeight).Padding(0, spaceS).Gap(spaceS).AlignItems(ui.Center).Radius(th.Radius).
		Role(ui.RoleButton).Cursor(ui.CursorPointer).Label(shortRef(r.ref))
	fg, k := th.Text, pickInk(th, hi)
	if hi {
		row.Background(th.Accent)
		fg = th.AccentText
	}
	row.Children(func() {
		if r.ref == t.s.State.Head.Target && !t.s.State.Head.Detached {
			row.Description(locales.BranchPickerCurrent())
			ui.Icon(c, glyphs().check).FontSize(iconMedium).Width(markW).TextColor(fg)
		} else {
			ui.Box(c).Width(markW)
		}
		ui.Text(c, shortRef(r.ref)).SingleLine().TruncateMiddle().Shrink(1).Grow(1).TextColor(fg)
		b, ok := p.info[r.ref]
		if !ok {
			return
		}
		if b.Worktree != "" {
			ui.Text(c, locales.RefsInWorktree(filepath.Base(b.Worktree))).FontSize(textSmall).TextColor(k.Muted).SingleLine().Description(locales.RefsCheckedOutIn(b.Worktree))
		}
		if b.Upstream != "" && !b.Gone {
			describeAheadBehind(row, b.Ahead, b.Behind)
			aheadBehind(c, k, b.Ahead, b.Behind, "")
		}
		// When it last moved, which the local branches are listed by.
		if b.Time > 0 {
			when := time.Unix(b.Time, 0)
			ui.Text(c, shortAgo(now, when)).FontSize(textSmall).TextColor(k.Muted).SingleLine().Description(since(now, when))
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
	for _, r := range p.orderHeads(t, sr) {
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

// orderHeads is the local branches as the picker lists them: the pinned
// first, as the sidebar has them, then the others by their last commit,
// the newest first, once the branches' state is read (indexInfo); by
// name until then.
func (p *branchPick) orderHeads(t *tab, sr *sideRefs) []string {
	key := headsKey{sr.refs, sr.gen, p.infoFor}
	if key == p.headsFor && p.heads != nil {
		return p.heads
	}
	p.headsFor, p.heads = key, slices.Clone(sr.heads)
	if p.info == nil {
		return p.heads
	}
	pinned := 0
	for pinned < len(p.heads) && slices.Contains(t.prefs.pinned, p.heads[pinned]) {
		pinned++
	}
	slices.SortStableFunc(p.heads[pinned:], func(x, y string) int {
		return cmp.Compare(p.info[y].Time, p.info[x].Time)
	})
	return p.heads
}

// shortAgo is how long before now t was in a few letters, for a narrow
// place: 5m, 3h, 2d, 4w, 3mo, 2y.
func shortAgo(now, t time.Time) string {
	const day = 24 * time.Hour
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return locales.TimeShortNow()
	case d < time.Hour:
		return locales.TimeShortMinutes(int(d / time.Minute))
	case d < day:
		return locales.TimeShortHours(int(d / time.Hour))
	case d < 7*day:
		return locales.TimeShortDays(int(d / day))
	case d < 30*day:
		return locales.TimeShortWeeks(int(d / (7 * day)))
	case d < 365*day:
		return locales.TimeShortMonths(int(d / (30 * day)))
	}
	return locales.TimeShortYears(int(d / (365 * day)))
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
