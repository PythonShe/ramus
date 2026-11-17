package ui

import (
	"sync"

	"github.com/egoist/mygo/ui"

	"ramus/locales"
)

// glyph parses an icon body drawn on a 24x24 grid with a 1.75 stroke,
// round caps and joins, in the text's color. thick is for glyphs shown
// at iconSmall, whose stroke must stay about a pixel wide.
func glyph(body string, thick bool) *ui.SVG {
	w := "1.75"
	if thick {
		w = "2.5"
	}
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="` + w + `" stroke-linecap="round" stroke-linejoin="round">` + body + `</svg>`))
}

// glyphSet is every icon of the app's own, in one style. Dots are
// filled circles, never lines of no length, which a renderer may drop.
type glyphSet struct {
	sidebar, plus, minus, gear, info *ui.SVG
	branch, repo, fork, folder       *ui.SVG
	personAdd, worktrees, external   *ui.SVG
	// chevrons are drawn at iconSmall, so they are thick.
	chevronDown, chevronRight, chevronLeft *ui.SVG

	// The sync button's and the ahead and behind counts'.
	arrowDown, arrowUp, refresh, cloud, cloudUp *ui.SVG

	// Marks: check and x are for iconMedium, checkThick and xThick for
	// iconSmall.
	check, checkThick, x, xThick, dot *ui.SVG
	warning, error, star, starFill    *ui.SVG

	// Actions and modes.
	copy, filter, swap, more, list, tree, eye *ui.SVG

	// Sections of the sidebar.
	changes, history, tag, stash, snapshot     *ui.SVG
	reflog, terminal, chart, pullRequest, lock *ui.SVG
}

// glyphs is the glyph set, parsed once.
var glyphs = sync.OnceValue(func() (g glyphSet) {
	const (
		dot       = ` fill="currentColor" stroke="none"/>`
		cloud     = `<path d="M7.5 18.5H17a3.5 3.5 0 0 0 .22-6.99A5 5 0 1 0 7.25 11.01 3.75 3.75 0 0 0 7.5 18.5z"/>`
		check     = `<path d="M5 12.5l4.5 4.5L19 7.5"/>`
		x         = `<path d="M7 7l10 10M17 7 7 17"/>`
		starShape = `<path d="M12 4l2.35 5.51 5.97.54-4.52 3.94 1.34 5.84L12 16.75l-5.14 3.08 1.34-5.84-4.52-3.94 5.97-.54z"`
	)
	g.sidebar = glyph(`<rect x="3" y="4.5" width="18" height="15" rx="3"/><path d="M9.5 4.5v15"/>`, false)
	g.plus = glyph(`<path d="M12 5.5v13M5.5 12h13"/>`, false)
	g.minus = glyph(`<path d="M5.5 12h13"/>`, false)
	// Eight teeth from a radius of 6.75 to 9.25 round a hole, in the
	// set's stroke: a dashed ring drew heavier than every glyph near it.
	g.gear = glyph(`<path d="M10.37 5.45L10.71 2.84A9.25 9.25 0 0 1 13.29 2.84L13.63 5.45A6.75 6.75 0 0 1 15.48 6.21L17.57 4.61A9.25 9.25 0 0 1 19.39 6.43L17.79 8.52A6.75 6.75 0 0 1 18.55 10.37L21.16 10.71A9.25 9.25 0 0 1 21.16 13.29L18.55 13.63A6.75 6.75 0 0 1 17.79 15.48L19.39 17.57A9.25 9.25 0 0 1 17.57 19.39L15.48 17.79A6.75 6.75 0 0 1 13.63 18.55L13.29 21.16A9.25 9.25 0 0 1 10.71 21.16L10.37 18.55A6.75 6.75 0 0 1 8.52 17.79L6.43 19.39A9.25 9.25 0 0 1 4.61 17.57L6.21 15.48A6.75 6.75 0 0 1 5.45 13.63L2.84 13.29A9.25 9.25 0 0 1 2.84 10.71L5.45 10.37A6.75 6.75 0 0 1 6.21 8.52L4.61 6.43A9.25 9.25 0 0 1 6.43 4.61L8.52 6.21A6.75 6.75 0 0 1 10.37 5.45z"/><circle cx="12" cy="12" r="3"/>`, false)
	g.info = glyph(`<circle cx="12" cy="12" r="9"/><path d="M12 11v5.5"/><circle cx="12" cy="7.75" r="1.2"`+dot, false)
	g.branch = glyph(`<circle cx="7" cy="5.5" r="2.25"/><circle cx="7" cy="18.5" r="2.25"/><circle cx="17" cy="7" r="2.25"/><path d="M7 7.75v8.5M17 9.25c0 4.5-10 3-10 7"/>`, false)
	g.repo = glyph(`<path d="M5 18.5V5.5A2.5 2.5 0 0 1 7.5 3H19v13H7.5A2.5 2.5 0 0 0 5 18.5zm0 0A2.5 2.5 0 0 0 7.5 21H19"/><path d="M9 7h6"/>`, false)
	g.fork = glyph(`<circle cx="6.5" cy="5.5" r="2.25"/><circle cx="17.5" cy="5.5" r="2.25"/><circle cx="12" cy="18.5" r="2.25"/><path d="M6.5 7.75v1.5c0 2 1.5 3 3 3h5c1.5 0 3-1 3-3v-1.5M12 12.25v4"/>`, false)
	g.folder = glyph(`<path d="M3.5 7.5a2 2 0 0 1 2-2h4l2 2.5h7a2 2 0 0 1 2 2v7.5a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2z"/>`, false)
	g.personAdd = glyph(`<circle cx="9.5" cy="8" r="3.5"/><path d="M3 19.5c0-3.6 2.9-6 6.5-6s6.5 2.4 6.5 6"/><path d="M19 8v6M16 11h6"/>`, false)
	g.worktrees = glyph(`<rect x="3" y="9" width="18" height="11" rx="2.5"/><path d="M5.5 6h13M8 3h8"/>`, false)
	g.external = glyph(`<path d="M13 5H7a2 2 0 0 0-2 2v10a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2v-6"/><path d="M15 4h5v5M20 4l-9 9"/>`, false)
	g.chevronDown = glyph(`<path d="M6 9.5l6 6 6-6"/>`, true)
	g.chevronRight = glyph(`<path d="M9.5 6l6 6-6 6"/>`, true)
	g.chevronLeft = glyph(`<path d="M14.5 6l-6 6 6 6"/>`, true)

	g.arrowDown = glyph(`<path d="M12 4.5v15M6 13.5l6 6 6-6"/>`, false)
	g.arrowUp = glyph(`<path d="M12 19.5v-15M6 10.5l6-6 6 6"/>`, false)
	// Two arcs of a radius of 7.5, ending at 140 and 320 degrees, each
	// with its head: it turns round its center while a fetch runs.
	g.refresh = glyph(`<path d="M19.5 12a7.5 7.5 0 0 1-13.24 4.82M6.26 20.32v-3.5h3.5"/><path d="M4.5 12a7.5 7.5 0 0 1 13.24-4.82M17.74 3.68v3.5h-3.5"/>`, false)
	g.cloud = glyph(cloud, false)
	g.cloudUp = glyph(cloud+`<path d="M12 16v-5.5M9.75 12.75 12 10.5l2.25 2.25"/>`, false)

	g.check = glyph(check, false)
	g.checkThick = glyph(check, true)
	g.x = glyph(x, false)
	g.xThick = glyph(x, true)
	g.dot = glyph(`<circle cx="12" cy="12" r="3.5"`+dot, false)
	g.warning = glyph(`<path d="M10.27 4.5a2 2 0 0 1 3.46 0l7.07 12.25a2 2 0 0 1-1.73 3H4.93a2 2 0 0 1-1.73-3z"/><path d="M12 9.5v4"/><circle cx="12" cy="16.6" r="1.2"`+dot, false)
	g.error = glyph(`<circle cx="12" cy="12" r="9"/><path d="M12 7.5v5.5"/><circle cx="12" cy="16.25" r="1.2"`+dot, false)
	g.star = glyph(starShape+`/>`, false)
	g.starFill = glyph(starShape+` fill="currentColor"/>`, false)

	g.copy = glyph(`<rect x="8.5" y="8.5" width="11" height="11" rx="2.25"/><path d="M15.5 8.5V6.75a2.25 2.25 0 0 0-2.25-2.25h-6.5A2.25 2.25 0 0 0 4.5 6.75v6.5a2.25 2.25 0 0 0 2.25 2.25H8.5"/>`, false)
	g.filter = glyph(`<path d="M4 7h16M7 12h10M10 17h4"/>`, false)
	g.swap = glyph(`<path d="M16.5 4.5 20 8l-3.5 3.5M20 8H8"/><path d="M7.5 12.5 4 16l3.5 3.5M4 16h12"/>`, false)
	g.more = glyph(`<circle cx="5.5" cy="12" r="1.5"`+dot+`<circle cx="12" cy="12" r="1.5"`+dot+`<circle cx="18.5" cy="12" r="1.5"`+dot, false)
	g.list = glyph(`<path d="M9 6.5h11M9 12h11M9 17.5h11"/><circle cx="4.75" cy="6.5" r="1.25"`+dot+`<circle cx="4.75" cy="12" r="1.25"`+dot+`<circle cx="4.75" cy="17.5" r="1.25"`+dot, false)
	g.tree = glyph(`<path d="M9 6.5h11M14 12h6M14 17.5h6"/><circle cx="4.75" cy="6.5" r="1.25"`+dot+`<circle cx="9.75" cy="12" r="1.25"`+dot+`<circle cx="9.75" cy="17.5" r="1.25"`+dot, false)
	g.eye = glyph(`<path d="M2.75 12S6.25 5.5 12 5.5 21.25 12 21.25 12 17.75 18.5 12 18.5 2.75 12 2.75 12z"/><circle cx="12" cy="12" r="2.75"/>`, false)

	g.changes = glyph(`<path d="M14 3.5H7.5a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2h9a2 2 0 0 0 2-2V8z"/><path d="M14 3.5V8h4.5"/><path d="M12 10.5v5M9.5 13h5M9.5 17.5h5"/>`, false)
	g.history = glyph(`<circle cx="12" cy="12" r="8.5"/><path d="M12 7.5V12l3 2"/>`, false)
	g.tag = glyph(`<path d="M3.5 11.6V5a1.5 1.5 0 0 1 1.5-1.5h6.6a1.5 1.5 0 0 1 1.06.44l7.4 7.4a1.5 1.5 0 0 1 0 2.12l-6.6 6.6a1.5 1.5 0 0 1-2.12 0l-7.4-7.4A1.5 1.5 0 0 1 3.5 11.6z"/><circle cx="8.25" cy="8.25" r="1.5"/>`, false)
	g.stash = glyph(`<path d="M4 13.5h4.5l1.5 2.5h4l1.5-2.5H20"/><path d="M6.6 5.5h10.8a1.5 1.5 0 0 1 1.4 1L20 13.5v4a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2v-4l1.2-7a1.5 1.5 0 0 1 1.4-1z"/>`, false)
	g.snapshot = glyph(`<path d="M4 8.5a2 2 0 0 1 2-2h2L9.5 4h5L16 6.5h2a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2z"/><circle cx="12" cy="13" r="3.5"/>`, false)
	g.reflog = glyph(`<path d="M4.5 12a7.5 7.5 0 1 0 2.2-5.3L4.5 9"/><path d="M4.5 4.5V9H9"/><path d="M12 8v4l2.75 1.75"/>`, false)
	g.terminal = glyph(`<rect x="3" y="4.5" width="18" height="15" rx="3"/><path d="M7 9.5l2.5 2.5L7 14.5M12 14.5h5"/>`, false)
	g.chart = glyph(`<path d="M6 19.5v-6M12 19.5v-15M18 19.5v-9"/>`, false)
	g.pullRequest = glyph(`<circle cx="6.5" cy="5.5" r="2.25"/><circle cx="6.5" cy="18.5" r="2.25"/><circle cx="17.5" cy="18.5" r="2.25"/><path d="M6.5 7.75v8.5M17.5 16.25V9.5a2.5 2.5 0 0 0-2.5-2.5h-3.5M13.5 4.5 11 7l2.5 2.5"/>`, false)
	g.lock = glyph(`<rect x="5" y="10.5" width="14" height="10" rx="2"/><path d="M8.5 10.5V7.5a3.5 3.5 0 0 1 7 0v3"/>`, false)
	return g
})

// iconButton is a button showing only g, named label for assistive
// technology and in a toolbar's overflow menu, which its tooltip shows.
// In a toolbar it has no face until hovered.
func iconButton(c *ui.Context, g *ui.SVG, label string) ui.Element {
	b := ui.Button(c, "").Label(label).Tooltip(label)
	b.Children(func() { ui.Icon(c, g).FontSize(iconLarge) })
	return b
}

// smallIconButton is a small muted icon beside text that acts when
// clicked, as Copy beside a hash, named label, which its tooltip says.
func smallIconButton(c *ui.Context, g *ui.SVG, label string) ui.Element {
	th := c.Theme()
	b := ui.ButtonBase(c).Label(label).Tooltip(label).Radius(th.Radius).Padding(space2XS).Shrink(0)
	if b.Hovered() {
		b.Background(th.SurfaceHover)
	}
	b.Children(func() { ui.Icon(c, g).FontSize(iconMedium).TextColor(th.TextMuted) })
	return b
}

// disclosure is the chevron of a folder or a section that opens: down
// while open, to the right while closed. It is thick, for iconSmall.
func disclosure(open bool) *ui.SVG {
	if open {
		return glyphs().chevronDown
	}
	return glyphs().chevronRight
}

// aheadBehind is how a branch stands with its upstream as glyphs and
// counts: up for the commits to push, down for those to pull, each left
// out at 0, so a branch even with its upstream shows nothing. Figures are
// tabular so that the width holds as the counts change. upstream is ""
// inside a control, as a picker's row, which assistive technology reads
// as one: the control then says the counts with describeAheadBehind.
func aheadBehind(c *ui.Context, k ink, ahead, behind int, upstream string) ui.Element {
	row := ui.Row(c).Gap(spaceM).AlignItems(ui.Center)
	if upstream != "" {
		row.Description(locales.WorktreeAheadBehind(ahead, behind, upstream))
	}
	return row.Children(func() {
		for _, p := range []struct {
			n int
			g *ui.SVG
		}{{ahead, glyphs().arrowUp}, {behind, glyphs().arrowDown}} {
			if p.n == 0 {
				continue
			}
			c.Key(p.g)
			ui.Row(c).Gap(space2XS).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, p.g).FontSize(iconMedium).TextColor(k.Muted)
				ui.Text(c, locales.SyncCount(p.n)).FontFeatures("tnum").TextColor(k.Muted)
			})
		}
	})
}

// describeAheadBehind tells assistive technology through e the commits
// to push and to pull that aheadBehind draws inside it.
func describeAheadBehind(e ui.Element, ahead, behind int) {
	if ahead > 0 {
		e.Description(locales.RepoPickerAheadLabel(ahead))
	}
	if behind > 0 {
		e.Description(locales.RepoPickerBehindLabel(behind))
	}
}

// pickInk is the colours of a picker's row, in the accent while hi.
func pickInk(th *ui.Theme, hi bool) ink {
	if hi {
		return chosenInk(th)
	}
	return plainInk(th)
}
