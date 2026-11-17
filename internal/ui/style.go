package ui

import (
	"github.com/egoist/mygo/ui"

	"ramus/locales"
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
	// barHeight is the least height of a pane's first row, its bar or
	// header, whether it holds controls or only text: panes side by side
	// put their first lines at one height.
	barHeight = controlHeight + 2*barPadY
	// messagePadY is the space above a commit's subject, which centres
	// its first line on a bar beside it.
	messagePadY = spaceL
)

// Type sizes. Body text takes the theme's size and sets none: it is 13 on
// macOS and 14 elsewhere.
const (
	textSmall   = 12
	textCaption = 11
	textDisplay = 28
)

// Icon sizes: a chevron after a title, an icon beside text in a row or
// a button, and a toolbar's icons and a file's.
const (
	iconSmall  = 10
	iconMedium = 14
	iconLarge  = 16
)

// statusLetterW is the width of a file's status letter, M or A, in a list,
// and statusLetterSmallW of one in textSmall.
const (
	statusLetterW      = spaceXL
	statusLetterSmallW = iconMedium
)

// markW is the column of a mark before a row's name, as the check by the
// branch checked out, which rows without one keep empty to line up.
const markW = iconMedium

// treeIndent is the indent of each level of a tree or a list of folders.
const treeIndent = spaceXL

// Corner radii beside the theme's Radius, which controls and rows take:
// a small label's, as a ref's, and a panel's that holds controls.
const (
	radiusSmall = 4
	radiusLarge = 10
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

// leastFieldW is the least a text field on a bar narrows to, its
// placeholder still read whole.
const leastFieldW = 160

// The widths of text fields on a bar that do not grow: for a date, for a
// name or a filter, and for a path.
const (
	fieldNarrow = 150
	fieldMedium = 220
	fieldWide   = 260
)

// textAreaH is the height of a text area for a message of a few lines,
// as a tag's.
const textAreaH = 96

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
// and builds its children. ui.Modal pads its panel already.
func dialogBox(c *ui.Context) ui.Element {
	return ui.Column(c).Gap(dialogGap)
}

// dangerButton is the button of an action that deletes or throws work
// away, in the theme's Danger color, so that it looks unlike one that
// makes something. Disabled, it is a plain button disabled.
func dangerButton(c *ui.Context, label string, disabled bool) ui.Element {
	th := c.Theme()
	b := ui.Button(c, label).Disabled(disabled)
	bg := th.Danger
	switch {
	case disabled:
		return b
	case b.Pressed():
		bg = bg.Mix(ui.RGBA(0, 0, 0, 1), 0.24)
	case b.Hovered():
		bg = bg.Mix(ui.RGBA(0, 0, 0, 1), 0.12)
	}
	return b.Background(bg).TextColor(th.AccentText).Border(1, bg)
}

// loading fills the space with a spinner, above what is being read when
// it can take long enough to wonder.
func loading(c *ui.Context, what string) {
	ui.Column(c).Fill().Center().Gap(spaceM).Children(func() {
		s := ui.Spinner(c).Label(locales.CommonLoading())
		if what != "" {
			s.Label(what)
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
	b.Children(func() { ui.Icon(c, glyphs().info).FontSize(iconMedium).TextColor(color) })
	if b.Clicked() {
		*open = !*open
	}
	ui.Popover(c, b, open, func() {
		ui.Text(c, text).FontSize(textSmall).MaxWidth(320).Selectable()
	})
}

// closeButton is the x that closes a tab or a pane, named label for
// assistive technology: without a face until hovered, so that it does not
// box in what it sits on.
func closeButton(c *ui.Context, label string) ui.Element {
	th := c.Theme()
	b := ui.ButtonBase(c).Label(label).Tooltip(label).Radius(radiusSmall).Padding(spaceXS)
	if b.Hovered() {
		// A wash of the text's color shows on whatever it sits on: a tab
		// hovered or chosen, or a pane's header.
		b.Background(th.Text.Alpha(0.12))
	}
	b.Children(func() { ui.Icon(c, glyphs().xThick).FontSize(iconSmall).TextColor(th.TextMuted) })
	return b
}
