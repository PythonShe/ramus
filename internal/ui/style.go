package ui

import "github.com/egoist/mygo/ui"

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

// errorMessage shows what went wrong where the content would be.
func errorMessage(c *ui.Context, msg string) {
	ui.Text(c, msg).TextColor(c.Theme().Danger).Padding(spaceL).Selectable()
}
