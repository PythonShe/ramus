package ui

import (
	"cmp"
	"context"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/redact"
	"ramus/internal/library"
	"ramus/internal/session"
	"ramus/locales"
)

// repoPick is the repository picker: the button in the bar naming the
// repository shown, and the panel below it that finds a known one,
// switches to it when open or opens it, and adds another.
type repoPick struct {
	open  bool
	query string
	// hi is the key of the row the arrows and the pointer are on, which
	// Enter chooses; rows move as briefs come in, the key stays.
	hi string
	// ptr is where the pointer was on the last frame: the pointer moving
	// onto a row takes hi there, but resting while the rows under it
	// change, as a query filters them or the picker opens, leaves hi to
	// the arrows.
	ptr  pointerAt
	list ui.ListState
	// briefs are what was read of each repository, by path; stop ends
	// the reading under way, and reading waits for it, for a test to
	// before the repositories' folders go.
	briefs  map[string]briefAt
	stop    context.CancelFunc
	reading sync.WaitGroup
}

type briefAt struct {
	session.RepoBrief
	at time.Time
}

// repoRow is a repository in the picker, or a heading. key tells apart
// a repository listed twice, as recent and under its owner.
type repoRow struct {
	key, path, name, heading string
}

const (
	repoPickWidth = 340
	repoPickRows  = 14 // shown before the list scrolls
	// repoPickRecent is how many recent repositories head the list.
	repoPickRecent = 3
	// briefFresh is how long a brief stands before the picker opening
	// reads it again; briefWorkers how many repositories are read at
	// once, and maxBriefs how many in all, the most recent.
	briefFresh   = time.Minute
	briefWorkers = 2
	maxBriefs    = 200
)

// repoButton names the repository shown and opens the picker.
func (a *App) repoButton(c *ui.Context, t *tab) {
	th := c.Theme()
	p := &a.repoPick
	name, icon := "", glyphs().repo
	if t != nil {
		name = a.title(t)
		if p.briefs[t.s.Path].Fork {
			icon = glyphs().fork
		}
	}
	label := locales.RepoPickerLabel(name)
	var b ui.Element
	ui.Toolbar(c, func() {
		b = ui.Button(c, "").Label(label).Tooltip(locales.RepoPickerTooltip())
		b.Children(func() {
			ui.Row(c).Gap(spaceS).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, icon).FontSize(iconMedium).TextColor(th.TextMuted)
				ui.Text(c, name).Bold().SingleLine().TruncateMiddle().MaxWidth(220)
				ui.Icon(c, glyphs().chevronDown).FontSize(iconSmall).TextColor(th.TextMuted)
			})
		})
		if b.Clicked() {
			if p.open {
				p.open = false
			} else {
				a.openRepoPicker()
			}
		}
	}).Label(label)
	if !p.open {
		a.stopBriefs()
		return
	}
	open, add := "", ""
	closing, moving := -1, -1
	var act func()
	ui.Popover(c, b, &p.open, func() {
		ui.Column(c).Width(repoPickWidth).Gap(spaceS).Children(func() {
			var f ui.Element
			ui.Row(c).Gap(spaceS).AlignItems(ui.Center).Children(func() {
				f = ui.SearchField(c, &p.query).Label(locales.RepoPickerSearch()).Placeholder(locales.RepoPickerSearch()).AutoFocus().Grow(1)
				ui.PopoverMenuButton(c, locales.RepoPickerAdd(), func(m *ui.Menu) {
					for _, it := range []struct{ id, label string }{
						{"open", locales.KeysOpen()}, {"clone", locales.KeysClone()}, {"new", locales.KeysNew()}, {"", ""},
						{"repositories", locales.KeysRepositories()},
					} {
						if it.id == "" {
							m.Separator()
						} else if m.Item(it.label).Chosen() {
							add = it.id
						}
					}
				})
			})
			rows := a.repoRows()
			at := slices.IndexFunc(rows, func(r repoRow) bool { return r.heading == "" && r.key == p.hi })
			if at < 0 {
				// Enter starts on a repository other than the one shown,
				// where it would do nothing; the shown one, when alone.
				cur := ""
				if t := a.current(); t != nil {
					cur = t.s.Path
				}
				at = slices.IndexFunc(rows, func(r repoRow) bool { return r.heading == "" && r.path != cur })
				if at < 0 {
					at = slices.IndexFunc(rows, func(r repoRow) bool { return r.heading == "" })
				}
				if at >= 0 {
					p.hi = rows[at].key
				}
			}
			move := func(d int) {
				for i := at + d; i >= 0 && i < len(rows); i += d {
					if rows[i].heading == "" {
						p.hi = rows[i].key
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
			if f.Submitted() && at >= 0 {
				open = rows[at].path
			}
			if len(rows) == 0 {
				ui.Text(c, locales.RepoPickerNone()).FontSize(textSmall).TextColor(th.TextMuted).Padding(spaceM, spaceS)
				return
			}
			hover := ""
			list := ui.List(c, &p.list, len(rows), func(i int) {
				r := a.repoRowView(c, rows[i], rows[i].heading == "" && rows[i].key == p.hi)
				if rows[i].heading != "" {
					return
				}
				// An open repository closes, and moves to a window of its
				// own, from its menu, as a tab did.
				if at := slices.IndexFunc(a.tabs, func(t *tab) bool { return t.s.Path == rows[i].path }); at >= 0 {
					ot := a.tabs[at]
					r.ContextMenu(func(m *ui.Menu) {
						if a.NewWindow != nil && m.Item(locales.TabsMoveToNewWindow()).Disabled(len(a.tabs) < 2 || busyWrite(ot) != nil).Chosen() {
							moving = at
						}
						a.editorMenu(m, ot, &act)
						m.Separator()
						if m.Item(locales.RepoPickerClose()).Chosen() {
							closing = at
						}
					})
				}
				if r.Clicked() {
					open = rows[i].path
				}
				if r.Hovered() {
					hover = rows[i].key
				}
			}).Label(locales.RepoPickerList()).FillWidth().Height(float32(min(len(rows), repoPickRows) * rowHeight))
			// Where the pointer is in the window, known once the list was
			// laid out: the list drawn under a pointer at rest is no move.
			x, y, _ := list.PointerPosition()
			b := list.Bounds()
			ptr := pointerAt{x: b.X + x, y: b.Y + y, known: b.W > 0 && b.H > 0}
			moved := p.ptr.known && ptr.known && ptr != p.ptr
			p.ptr = ptr
			if moved && hover != "" && hover != p.hi {
				// The rows are drawn: draw them again with the highlight
				// under the pointer, or it shows a move late.
				p.hi = hover
				c.AnimationFrame()
			}
		})
	})
	if act != nil {
		act()
	}
	switch {
	case closing >= 0:
		a.closeTab(closing)
	case moving >= 0:
		p.open = false
		a.moveToNewWindow(moving)
	case open != "":
		p.open = false
		a.Open(open)
	case add != "":
		p.open = false
		switch add {
		case "open":
			a.pickAndOpen()
		case "clone":
			a.clone.open = true
		case "new":
			a.initAndOpen()
		case "repositories":
			a.openManager()
		}
	}
	if !p.open {
		a.stopBriefs()
	}
}

// pointerAt is where the pointer is in the window, and whether that is
// known.
type pointerAt struct {
	x, y  float32
	known bool
}

// openRepoPicker opens the repository picker, empty of a query, and
// reads again the briefs gone stale.
func (a *App) openRepoPicker() {
	p := &a.repoPick
	p.open, p.query, p.hi, p.ptr = true, "", "", pointerAt{}
	a.readBriefs()
}

// readBriefs reads, a few at a time off the main thread, the briefs of
// the most recent repositories the library knows that are missing or
// stale, and forgets those of repositories it no longer knows.
func (a *App) readBriefs() {
	p := &a.repoPick
	if a.Library == nil || a.Runner == nil {
		return
	}
	a.stopBriefs()
	known := a.Library.Recent(maxBriefs)
	keep := make(map[string]briefAt, len(known))
	now := a.Now()
	var stale []string
	for _, r := range known {
		b, ok := p.briefs[r.Path]
		if ok {
			keep[r.Path] = b
		}
		if !ok || now.Sub(b.at) >= briefFresh {
			stale = append(stale, r.Path)
		}
	}
	p.briefs = keep
	if len(stale) == 0 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.stop = cancel
	runner, update := a.Runner, a.Update
	var trusted func(string) bool
	if ts := a.Trust; ts != nil {
		trusted = ts.Trusted
	}
	jobs := make(chan string, len(stale))
	for _, path := range stale {
		jobs <- path
	}
	close(jobs)
	for range min(briefWorkers, len(stale)) {
		p.reading.Add(1)
		a.goWork(nil, func() {
			defer p.reading.Done()
			for path := range jobs {
				if ctx.Err() != nil {
					return
				}
				b := session.ReadBrief(ctx, runner, trusted, path)
				if ctx.Err() != nil {
					return
				}
				update(func() {
					if ctx.Err() == nil {
						p.briefs[path] = briefAt{b, a.Now()}
					}
				})
			}
		})
	}
}

// stopBriefs ends the reading of briefs under way; what is read stays.
func (a *App) stopBriefs() {
	if p := &a.repoPick; p.stop != nil {
		p.stop()
		p.stop = nil
	}
}

// repoRows are the picker's rows. A query lists the repositories it
// finds, best first. Without one: those open in the window, the few
// opened last of the others, the favourites,
// then every repository under the group the user put it in, or else
// under the owner of its remote, as GitHub Desktop has them; one with
// no owner known goes under Other.
func (a *App) repoRows() []repoRow {
	p := &a.repoPick
	if a.Library == nil {
		return nil
	}
	row := func(under string, r library.Repo) repoRow {
		return repoRow{key: under + "\x00" + r.Path, path: r.Path, name: r.Name}
	}
	var rows []repoRow
	if q := strings.TrimSpace(p.query); q != "" {
		for _, r := range a.Library.Search(q) {
			rows = append(rows, row("", r))
		}
		return rows
	}
	section := func(heading string, repos []library.Repo) {
		if len(repos) == 0 {
			return
		}
		rows = append(rows, repoRow{heading: heading})
		for _, r := range repos {
			rows = append(rows, row(heading, r))
		}
	}
	current := ""
	if t := a.current(); t != nil {
		current = t.s.Path
	}
	all := a.Library.Recent(maxBriefs)
	// A repository open is always there to switch to, even before the
	// library has it written.
	for _, t := range a.tabs {
		if !slices.ContainsFunc(all, func(r library.Repo) bool { return r.Path == t.s.Path }) {
			all = append(all, library.Repo{Path: t.s.Path, Name: a.title(t)})
		}
	}
	// The repositories open in the window first, as their tabs are
	// ordered; the recent ones are those not open.
	var open []library.Repo
	for _, t := range a.tabs {
		open = append(open, library.Repo{Path: t.s.Path, Name: a.title(t)})
	}
	section(locales.RepoPickerOpenHeading(), open)
	var recent []library.Repo
	for _, r := range all {
		if len(recent) == repoPickRecent {
			break
		}
		if r.Path != current && !a.Has(r.Path) {
			recent = append(recent, r)
		}
	}
	section(locales.RepoPickerRecent(), recent)
	section(locales.RepoPickerFavourites(), a.Library.Favourites())

	byPath := make(map[string]library.Repo, len(all))
	for _, r := range all {
		byPath[r.Path] = r
	}
	byName := func(rs []library.Repo) []library.Repo {
		slices.SortFunc(rs, func(x, y library.Repo) int {
			return cmp.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name))
		})
		return rs
	}
	grouped := map[string]bool{}
	for _, g := range a.Library.Groups() {
		var in []library.Repo
		for _, path := range g.Paths {
			r, ok := byPath[path]
			if !ok {
				r = library.Repo{Path: path, Name: filepath.Base(path)}
			}
			grouped[path] = true
			in = append(in, r)
		}
		section(g.Name, byName(in))
	}
	owners := map[string][]library.Repo{}
	var other []library.Repo
	for _, r := range all {
		if grouped[r.Path] {
			continue
		}
		if o := p.briefs[r.Path].Owner; o != "" {
			owners[o] = append(owners[o], r)
		} else {
			other = append(other, r)
		}
	}
	names := make([]string, 0, len(owners))
	for o := range owners {
		names = append(names, o)
	}
	slices.SortFunc(names, func(x, y string) int { return cmp.Compare(strings.ToLower(x), strings.ToLower(y)) })
	for _, o := range names {
		section(o, byName(owners[o]))
	}
	section(locales.RepoPickerOther(), byName(other))
	return rows
}

// repoRowView builds a row of the picker: a heading, or a repository
// with a check by the one shown, its icon, for one open its branch and a
// dot while it has changes, and the commits its branch has to push and
// to pull.
func (a *App) repoRowView(c *ui.Context, r repoRow, hi bool) ui.Element {
	th := c.Theme()
	if r.heading != "" {
		return ui.Text(c, r.heading).FontSize(textCaption).Bold().TextColor(th.TextMuted).SingleLine().Height(rowHeight).Padding(spaceS+2, spaceS, 0, spaceS)
	}
	b := a.repoPick.briefs[r.path]
	row := ui.Row(c).Height(rowHeight).Padding(0, spaceS).Gap(spaceS).AlignItems(ui.Center).Radius(th.Radius).
		Role(ui.RoleButton).Cursor(ui.CursorPointer).Label(r.name).Tooltip(r.path)
	fg, k := th.Text, pickInk(th, hi)
	if hi {
		row.Background(th.Accent)
		fg = th.AccentText
	}
	var open *tab
	if at := slices.IndexFunc(a.tabs, func(t *tab) bool { return t.s.Path == r.path }); at >= 0 {
		open = a.tabs[at]
	}
	row.Children(func() {
		// A check by the repository shown; the others open are in their
		// own group.
		if cur := a.current(); cur != nil && cur.s.Path == r.path {
			row.Description(locales.RepoPickerShown())
			ui.Icon(c, glyphs().check).FontSize(iconMedium).Width(markW).TextColor(fg)
		} else {
			if open != nil {
				row.Description(locales.RepoPickerOpen())
			}
			ui.Box(c).Width(markW)
		}
		icon := glyphs().repo
		if b.Fork {
			icon = glyphs().fork
		}
		ui.Icon(c, icon).FontSize(iconMedium).TextColor(k.Muted)
		ui.Text(c, r.name).SingleLine().TruncateMiddle().Shrink(1).Grow(1).TextColor(fg)
		if open != nil {
			// Its branch, and a dot while it has changes, as its tab has.
			head := open.s.State.Head
			branch := strings.TrimPrefix(head.Target, "refs/heads/")
			if head.Detached {
				branch = locales.BranchPickerDetached()
			}
			ui.Text(c, redact.Control(branch)).FontSize(textSmall).TextColor(k.Muted).SingleLine().TruncateMiddle().Shrink(2)
			state, write := tabState(open)
			switch state {
			case "running":
				row.Description(locales.RepoPickerRunning(write))
			case "changed":
				row.Description(locales.RepoPickerChanged())
			}
			tabMark(c, state, write, k.Muted)
		}
		describeAheadBehind(row, b.Ahead, b.Behind)
		aheadBehind(c, k, b.Ahead, b.Behind, "")
	})
	return row
}
