package ui

import (
	"sync"

	"github.com/egoist/mygo/ui"
)

// The app's spacing, type sizes and shared frames. Views take their
// numbers from here, never literals, so the same kind of thing looks the
// same in every view.

// The spacing steps.
const (
	space2XS = 2
	spaceXS  = 4
	spaceS   = 6
	spaceM   = 8
	spaceL   = 12
	spaceXL  = 16
	space2XL = 20
	space3XL = 24
)

// Spacing by role.
const (
	// insetX is every pane's left and right inset, so bars, rows,
	// headers and messages in a pane line up.
	insetX = spaceL
	// barPadY pads a toolbar or a pane's header.
	barPadY = spaceS
	// rowPadY pads a one-line list row; rowTallPadY a row of two lines.
	rowPadY     = spaceXS
	rowTallPadY = spaceS
	// bannerPadY pads a band across the top of a pane that asks
	// something: trust, force push, a commit-graph to write.
	bannerPadY = spaceM
	dialogPad  = space2XL
	dialogGap  = spaceL
	// rowHeight is a one-line list row: a line of text and rowPadY
	// above and below it.
	rowHeight = 24
	// controlHeight is a toolbar button's height, which what sits on a
	// bar beside toolbar buttons, as a tab, takes too.
	controlHeight = 28
)

// Type sizes. Body text takes the theme's size and sets none: it is 13 on
// macOS and 14 elsewhere.
const (
	textSmall   = 12
	textCaption = 11
	textDisplay = 28
)

// Dialog widths. A dialog whose content sets its size, a chart or a
// table, gives its own.
const (
	dialogNarrow = 460
	dialogMedium = 540
	dialogWide   = 640
)

// The least widths of the panes a split shares a view between, which the
// first pane gives way to as the window narrows: a list beside what it
// shows of the row chosen, a commit beside its patch, and that patch.
const (
	leastListW  = 240
	leastInfoW  = 200
	leastPatchW = 300
)

// fitPane is the width the first pane of a split takes of room, the
// split's width as last laid out: size, as the user set it, cut for the
// second pane to keep keep, but never under least. Before the split was
// laid out, room is 0 and size is taken as it is.
func fitPane(size, room, least, keep float32) float32 {
	if room <= 0 {
		return size
	}
	// The divider between them is a point wide.
	return max(least, min(size, room-1-keep))
}

// keptSplit is ui.Split with its first pane given way, as the room
// narrows, for the second to keep keep: *size stays as the user set it,
// for when there is room again, and a drag sets it. *room keeps the
// split's width as last laid out.
func keptSplit(c *ui.Context, size, room *float32, least, keep float32, first, second func()) ui.Element {
	v := fitPane(*size, *room, least, keep)
	sp := ui.Split(c, &v, first, second)
	if sp.Changed() {
		*size = v
	}
	if w := sp.LayoutBounds().W; w != *room {
		// The panes take the new room in the next frame.
		*room = w
		c.AnimationFrame()
	}
	return sp
}

// dialogBox is the frame inside every dialog; the caller sets its width
// and builds its children.
func dialogBox(c *ui.Context) ui.Element {
	return ui.Column(c).Gap(dialogGap).Padding(dialogPad)
}

// loading fills the space with a spinner, above what is being read when
// it can take long enough to wonder.
func loading(c *ui.Context, what string) {
	ui.Column(c).Fill().Center().Gap(spaceM).Children(func() {
		ui.Spinner(c)
		if what != "" {
			ui.Text(c, what).FontSize(textSmall).TextColor(c.Theme().TextMuted)
		}
	})
}

// emptyState fills the space with why there is nothing to show.
func emptyState(c *ui.Context, msg string) {
	ui.Column(c).Fill().Center().Children(func() {
		ui.Text(c, msg).TextColor(c.Theme().TextMuted).Selectable()
	})
}

// emptyPane fills the space of a pane with nothing chosen to show.
func emptyPane(c *ui.Context) {
	ui.Column(c).Fill()
}

// errorMessage shows what went wrong where the content would be.
func errorMessage(c *ui.Context, msg string) {
	ui.Text(c, msg).TextColor(c.Theme().Danger).Padding(spaceL).Selectable()
}

// clickable makes a text act when clicked, as a link does: the pointer
// and an underline under it while hovered say so, and it takes the focus
// for Enter and Space. The underline sits below the descenders, so the
// line that skips their ink runs whole.
func clickable(e ui.Element) ui.Element {
	e = e.Cursor(ui.CursorPointer).Focusable().Role(ui.RoleButton)
	if e.Hovered() {
		e = e.Underline().UnderlineOffset(3)
	}
	return e
}

// infoButton is a small icon, in color, that opens more to say in a
// popover: what would crowd a row as a line of text, as git's reason a
// signature could not be checked. *open keeps the popover open.
func infoButton(c *ui.Context, open *bool, label, text string, color ui.Color) {
	th := c.Theme()
	b := ui.ButtonBase(c).Label(label).Tooltip(label).Radius(th.Radius).Padding(space2XS)
	if b.Hovered() {
		b.Background(th.SurfaceHover)
	}
	b.Children(func() { ui.Icon(c, styleIcons().info).FontSize(textSmall + 2).TextColor(color) })
	if b.Clicked() {
		*open = !*open
	}
	ui.Popover(c, b, open, func() {
		ui.Text(c, text).FontSize(textSmall).MaxWidth(320).Selectable()
	})
}

// styleIcons are the shared controls' icons, parsed once.
var styleIcons = sync.OnceValue(func() (icons struct{ info *ui.SVG }) {
	icons.info = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round"><circle cx="12" cy="12" r="9"/><path d="M12 11v5.5"/><path d="M12 7.6v.1" stroke-width="2.4"/></svg>`))
	return icons
})

// closeButton is the × that closes a tab or a pane, named label for
// assistive technology: without a face until hovered, so that it does not
// box in what it sits on.
func closeButton(c *ui.Context, label string) ui.Element {
	th := c.Theme()
	b := ui.ButtonBase(c).Label(label).Tooltip(label).Radius(4).Padding(0, spaceXS)
	if b.Hovered() {
		// A wash of the text's color shows on whatever it sits on: a tab
		// hovered or chosen, or a pane's header.
		b.Background(th.Text.Alpha(0.12))
	}
	b.Children(func() { ui.Text(c, "×").TextColor(th.TextMuted) })
	return b
}
