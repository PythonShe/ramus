package ui

import (
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"

	"ramus/internal/session"
	"ramus/locales"
)

// coAuthorPick is the commit box's picker of co-authors: the people who
// wrote the repository's recent commits, searched by name or address,
// and anyone typed in whole as Name <email>.
type coAuthorPick struct {
	open  bool
	query string
	// hi is the row the arrows and the pointer are on, which Enter
	// chooses; hover the row under the pointer, -1 none, as in the
	// branch picker.
	hi, hover int
	list      ui.ListState
	// people are those LoadAuthors read, once read.
	people []session.Person
	read   bool
	// rows are the people matching query and not chosen yet, rebuilt as
	// either changes.
	rows             []coAuthorRow
	rowsWith         string
	rowsRead, rowsOK bool
}

// coAuthorRow is a person in the picker: one of the authors, or one
// typed in, who is added as typed.
type coAuthorRow struct {
	name, email string
	typed       bool
}

func (r coAuthorRow) person() string { return r.name + " <" + r.email + ">" }

// typedPerson is a query that names someone whole.
var typedPerson = regexp.MustCompile(`^([^<>,;\n]+)<([^<>\s]+)>$`)

// coAuthorButton opens the picker, beside the summary.
func (a *App) coAuthorButton(c *ui.Context, t *tab) {
	th := c.Theme()
	cs := &t.commit
	p := &cs.pick
	label := locales.CommitBoxAddCoAuthor()
	b := ui.Button(c, "").Label(label).Tooltip(label).Disabled(cs.busy)
	b.Children(func() { ui.Icon(c, coAuthorIcons().add).FontSize(iconMedium).TextColor(th.TextMuted) })
	if b.Clicked() {
		if p.open {
			p.open = false
		} else {
			p.open, p.query, p.hi, p.hover, p.rowsOK = true, "", -1, -1, false
			t.s.LoadAuthors(func(people []session.Person) { p.people, p.read = people, true })
		}
	}
	if !p.open {
		return
	}
	var chosen *coAuthorRow
	ui.Popover(c, b, &p.open, func() {
		ui.Column(c).Width(pickWidth).Gap(spaceS).Children(func() {
			var f ui.Element
			ui.Row(c).Gap(spaceS).AlignItems(ui.Center).Children(func() {
				f = ui.SearchField(c, &p.query).Label(locales.CommitBoxSearchPeople()).Placeholder(locales.CommitBoxSearchPeople()).AutoFocus().Grow(1)
				if !p.read {
					ui.Spinner(c).FontSize(textSmall)
				}
			})
			rows := p.build(cs, a.Settings.Emails)
			switch {
			case f.Shortcut(0, ui.KeyDown):
				p.hi = min(p.hi+1, len(rows)-1)
				p.list.ScrollIntoView(p.hi)
			case f.Shortcut(0, ui.KeyUp):
				p.hi = max(p.hi-1, min(0, len(rows)-1))
				p.list.ScrollIntoView(p.hi)
			}
			if f.Submitted() && p.hi >= 0 && p.hi < len(rows) {
				chosen = &rows[p.hi]
			}
			if len(rows) == 0 {
				if p.read && p.query != "" {
					ui.Text(c, locales.CommitBoxNoPeople()).FontSize(textSmall).TextColor(th.TextMuted).Padding(spaceM, spaceS)
				}
				return
			}
			hover := -1
			ui.List(c, &p.list, len(rows), func(i int) {
				r := coAuthorRowView(c, rows[i], i == p.hi)
				if r.Clicked() {
					chosen = &rows[i]
				}
				if r.Hovered() {
					hover = i
				}
			}).Label(locales.CommitBoxPeople()).FillWidth().Height(float32(min(len(rows), pickRows) * rowHeight))
			if hover != p.hover {
				p.hover = hover
				if hover >= 0 {
					p.hi = hover
				}
			}
		})
	})
	if chosen != nil {
		p.open = false
		cs.coAuthors = strings.Join(append(coAuthorList(cs.coAuthors), chosen.person()), ", ")
	}
}

// coAuthorRowView builds a row of the picker: a person's name and
// address, or someone typed in to add.
func coAuthorRowView(c *ui.Context, r coAuthorRow, hi bool) ui.Element {
	th := c.Theme()
	label := r.name
	if r.typed {
		label = locales.CommitBoxAddPerson(r.person())
	}
	row := ui.Row(c).Height(rowHeight).Padding(0, spaceS).Gap(spaceS).AlignItems(ui.Center).Radius(th.Radius).
		Role(ui.RoleButton).Cursor(ui.CursorPointer).Label(label)
	fg, muted := th.Text, th.TextMuted
	if hi {
		row.Background(th.Accent)
		fg, muted = th.AccentText, th.AccentText
	}
	row.Children(func() {
		if r.typed {
			ui.Text(c, label).SingleLine().TruncateMiddle().Shrink(1).TextColor(fg)
			return
		}
		ui.Text(c, r.name).SingleLine().Shrink(0).TextColor(fg)
		ui.Text(c, r.email).FontSize(textSmall).SingleLine().TruncateMiddle().Shrink(1).TextColor(muted)
	})
	return row
}

// build is the people matching the query, but those chosen and the
// user's own addresses; first, someone typed in whole who is not among
// them.
func (p *coAuthorPick) build(cs *commitState, mine []string) []coAuthorRow {
	with := p.query + "\x00" + cs.coAuthors
	if p.rowsOK && p.rowsWith == with && p.rowsRead == p.read {
		return p.rows
	}
	p.rowsOK, p.rowsWith, p.rowsRead, p.rows = true, with, p.read, nil
	skip := map[string]bool{}
	for _, e := range mine {
		skip[strings.ToLower(e)] = true
	}
	for _, s := range coAuthorList(cs.coAuthors) {
		if m := typedPerson.FindStringSubmatch(s); m != nil {
			skip[strings.ToLower(m[2])] = true
		}
	}
	words := strings.Fields(strings.ToLower(p.query))
	for _, pp := range p.people {
		if skip[strings.ToLower(pp.Email)] {
			continue
		}
		s := strings.ToLower(pp.Name + " " + pp.Email)
		if !slices.ContainsFunc(words, func(w string) bool { return !strings.Contains(s, w) }) {
			p.rows = append(p.rows, coAuthorRow{name: pp.Name, email: pp.Email})
		}
	}
	if m := typedPerson.FindStringSubmatch(strings.TrimSpace(p.query)); m != nil {
		r := coAuthorRow{name: strings.TrimSpace(m[1]), email: m[2], typed: true}
		known := slices.ContainsFunc(p.rows, func(x coAuthorRow) bool { return strings.EqualFold(x.email, r.email) })
		if !known && !skip[strings.ToLower(r.email)] {
			p.rows = append([]coAuthorRow{r}, p.rows...)
		}
	}
	p.hi = min(0, len(p.rows)-1)
	return p.rows
}

// coAuthorChips are the co-authors chosen, each with a button taking it
// off.
func coAuthorChips(c *ui.Context, cs *commitState) {
	people := coAuthorList(cs.coAuthors)
	if len(people) == 0 {
		return
	}
	th := c.Theme()
	remove := -1
	ui.Row(c).Gap(spaceXS).Wrap().FillWidth().Label(locales.CommitBoxCoAuthors()).Children(func() {
		for i, s := range people {
			name := s
			if m := typedPerson.FindStringSubmatch(s); m != nil {
				name = strings.TrimSpace(m[1])
			}
			c.Key(s)
			ui.Row(c).Gap(space2XS).AlignItems(ui.Center).Padding(space2XS, space2XS, space2XS, spaceS).
				Radius(th.Radius).Background(th.Background).Border(1, th.Border).Tooltip(s).Children(func() {
				ui.Text(c, name).FontSize(textSmall).SingleLine()
				if closeButton(c, locales.CommitBoxRemoveCoAuthor(name)).Disabled(cs.busy).Clicked() {
					remove = i
				}
			})
		}
	})
	if remove >= 0 {
		cs.coAuthors = strings.Join(slices.Delete(people, remove, remove+1), ", ")
	}
}

// coAuthorIcons are the picker's icons, parsed once.
var coAuthorIcons = sync.OnceValue(func() (icons struct{ add *ui.SVG }) {
	// A person, and a plus beside them.
	icons.add = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round"><circle cx="9.5" cy="8" r="3.5"/><path d="M3 19.5c0-3.6 2.9-6 6.5-6s6.5 2.4 6.5 6"/><path d="M19 8v6M16 11h6"/></svg>`))
	return icons
})
