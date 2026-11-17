package ui

import (
	"maps"
	"math"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/lanes"
	"ramus/internal/settings"
	"ramus/internal/ui/graph"
	"ramus/locales"
)

// The history list's columns: the graph, the message, the author, the
// date and the hash. A divider in the header belongs to the column on its
// left, as in Finder: dragged right, it widens the column and pushes the
// ones after it into the free room at the end of the rows, and stops when
// there is none; dragged left, it narrows the column and draws the ones
// after it along, leaving that room free.
//
// The message fills the rows until the user gives it a width: by its own
// divider, down to its least, or by narrowing a column after it, which
// keeps it as wide as it was. It then keeps that width however wide the
// list, the rest free at the end; a double click on its divider has it
// fill the rows again, as does widening it to the end.
//
// The date and the hash always take as long, so they keep their widths:
// the hash has no divider, and the date's does not resize it.
//
// A drag resizes a column, down to its least width and no further, so
// that none shows part of its values. A click on a column's title shuts
// it; in its place stays a thin column between the divider before it and
// its own, which a click opens again, as does the header's menu.
//
// The graph does not shut but collapses to a thin form, its lanes at
// their closest, which still shows where history branches and merges: a
// click on it, or on its divider when thin, collapses or expands it.
// Narrower than its lanes need, the graph draws them closer together
// rather than cutting them off.
//
// The message is what a row is read by. It would like the room its
// subjects take, up to historyMessageWant, or the width the user gave it
// if less: when the list is too narrow for that, the free room at the end
// goes first, then the columns shut, the author, then the date, and then
// the graph goes thin; they come back as the list widens. Only then does
// the message narrow, down to historyMessageMin, short of which the hash
// shuts too. Nor does a drag widen a column into the room the message
// would like.

type histColumn int

const (
	colGraph histColumn = iota
	colAuthor
	colDate
	colHash
	histColumns
	// colMessage names the message's divider; the message takes the
	// room the others leave.
	colMessage = histColumns
)

// histColumnKeys name the columns in the settings.
var histColumnKeys = [histColumns]string{"graph", "author", "date", "hash"}

// The columns' own widths and the least they settle at, the date's and
// the hash's showing them whole, the author's a picture and a name; the
// graph's are graphWidth and graphLeast.
var (
	histColumnWidth = [histColumns]float32{0, 120 + authorPicture, 112, 64}
	histColumnMin   = [histColumns]float32{0, 80 + authorPicture, 112, 64}
)

// authorPicture is what the author's picture takes of their column.
const authorPicture = avatarRow + spaceXS

const (
	// historyMessageMin is the least the message narrows to;
	// historyMessageWant the most room it asks before the columns give
	// way, so that one long subject does not shut them all.
	historyMessageMin  = 200
	historyMessageWant = 440
	// historyGraphMax bounds a graph widened by hand.
	historyGraphMax = 320
)

// graphThin is the width of the graph's thin form: its lanes at their
// closest, up to five of them.
func graphThin(snap *lanes.Snapshot) float32 {
	n := float32(max(2, min(5, snap.Width())))
	return n * graph.MinLaneWidth
}

// graphLeast is the least width the full graph settles at, clear of the
// thin form's.
func graphLeast(snap *lanes.Snapshot) float32 {
	return min(graphWidth(snap), 2*graphThin(snap))
}

// columnSettle is how a column comes to rest: let go, shown, shut or
// given way. It carries on at the speed the drag left it.
var columnSettle = ui.Animation{Spring: ui.SpringOptions{Duration: 350 * time.Millisecond}}

// histLayout is where the columns are in a frame.
type histLayout struct {
	shown [histColumns]bool
	w     [histColumns]float32
	// end is the free room at the end of the rows.
	end float32
	// least is each column's least width; the graph's is its thin
	// form's, under which it fades.
	least [histColumns]float32
	// lanes is how many lanes the graph has.
	lanes int
	// handle marks the columns whose divider stays as they shut: those
	// the user shut, to drag out again, and the one being dragged.
	handle [histColumns]bool
	// to is where the columns go.
	to colWant
}

// colWant is where the columns go: their widths, the message's and the
// free room at the end; subj is the room the message's subjects would
// like, and room what the message has over the room it would like.
type colWant struct {
	w                    [histColumns]float32
	msg, end, subj, room float32
	// gaveWay is set when a column shut, or the graph went thin, for
	// want of room.
	gaveWay bool
}

// graphStyle draws the graph as wide as it is.
func (l *histLayout) graphStyle() graph.Style {
	return graph.DefaultStyle.Fit(l.w[colGraph], l.lanes)
}

// gap is the space after column k: its divider's, which closes with
// the column, or, shut by the user, the thin column in its place. The
// hash, the last, has no divider.
func (l *histLayout) gap(k histColumn) float32 { return columnGap(k, l.w[k], l.handle[k]) }

func columnGap(k histColumn, w float32, handle bool) float32 {
	switch {
	case handle:
		return shutWidth + spaceM
	case k == colHash:
		return 0
	}
	return min(spaceM, w)
}

// messageW is the width of the message's cell in a row room wide, as the
// columns are in this frame: what they and the row's insets leave. Before
// the list was laid out, room is 0, and the message has the room it would
// like.
func (l *histLayout) messageW(room float32) float32 {
	if room <= 0 {
		return historyMessageWant
	}
	w := room - 2*insetX - l.end - spaceM
	for k := range histColumns {
		if l.shown[k] {
			w -= l.w[k] + l.gap(k)
		} else if l.handle[k] {
			w -= l.gap(k)
		}
	}
	return w
}

// fade is how opaque column k is: it fades under its least width.
func (l *histLayout) fade(k histColumn) float32 {
	return min(1, l.w[k]/l.least[k])
}

// colDrag is a divider of the header being dragged.
type colDrag struct {
	active bool
	col    histColumn
	// raw is the width the pointer asks for, as far as it went; w0 the
	// column's as the press began, and msg0, end0 and room0 the layout's
	// msg, end and room then (colWant).
	raw, w0, msg0, end0, room0 float32
	// moved is set once the pointer moved, for a click on the graph's
	// divider not to be taken for a drag; done as the pointer lets go.
	moved, done bool
}

// historyHeader heads the columns and lays them out for the rows. Its
// dividers take this frame's drag before the widths are settled, so the
// divider, the titles and the rows keep up with the pointer.
func (a *App) historyHeader(c *ui.Context, hv *historyState, snap *lanes.Snapshot) {
	th := c.Theme()
	var titles, dividers [histColumns]ui.Element
	// Built are the columns shown, opening or still shutting.
	var built [histColumns]bool
	to := a.columnWidths(hv, snap)
	handles := a.columnHandles(hv)
	for k := range histColumns {
		built[k] = to.w[k] >= 0.5 || hv.cols.shown[k] || handles[k]
	}
	// No gap: the dividers stand in the rows' gaps.
	head := ui.Row(c.Key("columns")).Height(rowHeight).Padding(0, insetX).AlignItems(ui.Center).FillWidth()
	head.Children(func() {
		if built[colGraph] {
			titles[colGraph] = columnTitle(c, "")
			dividers[colGraph] = a.columnDivider(c, hv, colGraph)
		}
		columnTitle(c, locales.HistoryColumnMessage()).Grow(1).Shrink(1)
		a.columnDivider(c, hv, colMessage).Width(spaceM)
		for k := colAuthor; k < histColumns; k++ {
			if built[k] {
				titles[k] = columnTitle(c, histColumnName(k))
				if hasDivider(k, handles) {
					dividers[k] = a.columnDivider(c, hv, k)
				}
			}
		}
	})
	// Ruled off from the rows, as a Table's header is.
	ui.Divider(c)
	l := a.historyColumns(head, hv, snap)
	hv.cols = l
	head.PaddingEnd(insetX + l.end)
	for k := range histColumns {
		if !built[k] {
			continue
		}
		titles[k].Width(l.w[k]).Opacity(l.fade(k)).Hidden(!l.shown[k])
		if hasDivider(k, handles) {
			dividers[k].Width(l.gap(k)).Hidden(!l.shown[k] && !l.handle[k])
		}
		p := a.Settings.HistoryColumns[histColumnKeys[k]]
		if k == colGraph {
			// Thin for want of room, a click could not widen it.
			if !p.Thin && l.to.w[k] <= l.least[k] {
				continue
			}
			label := locales.HistoryGraphCollapse()
			if p.Thin {
				label = locales.HistoryGraphExpand()
			}
			titles[k].Label(label)
		}
		if !l.shown[k] || p.Hidden {
			continue
		}
		titles[k].Role(ui.RoleButton)
		// Still opening, it takes no click: the second of a double click
		// on the thin column it opened from lands on it.
		if titles[k].Clicked() && abs(l.w[k]-l.to.w[k]) < 0.5 {
			a.collapseColumn(k)
		}
		if titles[k].Hovered() {
			// A box behind the title, into the gaps at its sides short of
			// the dividers, says a click acts on it.
			titles[k].Draw(func(p *ui.Painter, r ui.Rect) {
				side := float32(hoverSide)
				hoverBox(p, ui.Rect{X: r.X - side, Y: r.Y, W: r.W + 2*side, H: r.H}, th.SurfaceHover)
			})
		}
	}
	head.ContextMenu(func(m *ui.Menu) {
		// The graph does not shut; it collapses, below.
		for k := colAuthor; k < histColumns; k++ {
			p := a.Settings.HistoryColumns[histColumnKeys[k]]
			if m.Item(histColumnName(k)).Checked(!p.Hidden).Chosen() {
				a.showColumn(k, p.Hidden)
			}
		}
		m.Separator()
		if p := a.Settings.HistoryColumns[histColumnKeys[colGraph]]; !p.Hidden {
			label := locales.HistoryGraphCollapse()
			if p.Thin {
				label = locales.HistoryGraphExpand()
			}
			if m.Item(label).Chosen() {
				a.toggleThinGraph()
			}
		}
		if m.Item(locales.HistoryColumnsReset()).Disabled(len(a.Settings.HistoryColumns) == 0 && a.Settings.HistoryMessage == 0).Chosen() {
			a.Settings.HistoryColumns, a.Settings.HistoryMessage = nil, 0
			a.keepSettings()
		}
	})
}

// historyColumns lays the columns out for the list as wide as it was in
// the last frame, their widths animated on head. Only what the user does
// to a column animates: the list narrowing or widening, as a file's
// patch opens beside the commit, the sidebar or the window is resized,
// moves the columns at once, as mygo has a window's resizing do; animated,
// each change of room had the graph and the message sweep across.
func (a *App) historyColumns(head ui.Element, hv *historyState, snap *lanes.Snapshot) histLayout {
	d := &hv.colDrag
	room := hv.roomW()
	resized := hv.colRoom != room
	hv.colRoom = room
	want := a.columnWidths(hv, snap)
	if d.active && d.done {
		a.letGo(d, want, snap)
		*d = colDrag{}
		want = a.columnWidths(hv, snap)
	}
	l := histLayout{least: histColumnMin, lanes: snap.Width(), handle: a.columnHandles(hv), to: want}
	l.least[colGraph] = graphThin(snap)
	for k := range histColumns {
		if d.active && d.col == k || resized {
			l.w[k] = head.Jump(k, want.w[k])
		} else {
			l.w[k] = head.AnimateTo(k, want.w[k], columnSettle)
		}
		// Under half a point a column is gone.
		l.shown[k] = l.w[k] >= 0.5
	}
	if d.active || resized {
		l.end = head.Jump("end", want.end)
	} else {
		l.end = head.AnimateTo("end", want.end, columnSettle)
	}
	return l
}

// hasDivider is whether column k has a divider: all but the hash, which
// has one only as it is shut, to bring it back.
func hasDivider(k histColumn, handles [histColumns]bool) bool {
	return k != colHash || handles[k]
}

// letGo settles the column dragged as the pointer lets go, where the
// pointer left it: want.
func (a *App) letGo(d *colDrag, want colWant, snap *lanes.Snapshot) {
	if !d.moved {
		return // a click, which the divider took
	}
	k := d.col
	if k == colMessage {
		if want.gaveWay && want.end >= 0.5 {
			// Too little room made for a column given way to come
			// back whole: the message takes it back.
			return
		}
		a.keepMessage(want.msg, want.end, want.subj)
		return
	}
	p := a.Settings.HistoryColumns[histColumnKeys[k]]
	if k == colGraph {
		// A thin graph, thin by the user's choice or for want of room,
		// let go nearer its thin form than the full graph's least stays
		// as it was. The message is left as it was.
		thin, least := graphThin(snap), graphLeast(snap)
		if want.w[k] < (thin+least)/2 {
			return
		}
		p.Thin, p.Width = false, max(least, want.w[k])
		a.setHistoryColumn(k, p)
		return
	}
	if k == colAuthor {
		p.Width = want.w[k]
	}
	// The message stays as wide as it was, the room the column took or
	// left free at the end.
	a.keepMessage(d.msg0, want.end, want.subj)
	a.setHistoryColumn(k, p)
}

// keepMessage keeps the message at width msg, with end free after the
// columns: none, and the message wide enough for its subjects, it fills
// the rows again.
func (a *App) keepMessage(msg, end, subj float32) {
	a.Settings.HistoryMessage = msg
	if end < 0.5 && msg >= subj {
		a.Settings.HistoryMessage = 0
	}
	a.keepSettings()
}

// columnHandles are the columns the user shut, which keep a thin column
// in their place to open them again.
func (a *App) columnHandles(hv *historyState) (handles [histColumns]bool) {
	for k := range histColumns {
		handles[k] = a.Settings.HistoryColumns[histColumnKeys[k]].Hidden
	}
	return handles
}

// columnWidths are where the columns go: the widths the user gave them
// or their own, those at the end given way where the list is too narrow
// for the message, and the dragged one's under the pointer, in the room
// at the end.
func (a *App) columnWidths(hv *historyState, snap *lanes.Snapshot) colWant {
	var cw colWant
	for k := range histColumns {
		p := a.Settings.HistoryColumns[histColumnKeys[k]]
		switch {
		case p.Hidden:
		case k == colGraph && p.Thin:
			cw.w[k] = graphThin(snap)
		case k == colGraph && p.Width > 0:
			cw.w[k] = min(p.Width, historyGraphMax)
		case k == colGraph:
			cw.w[k] = graphWidth(snap)
		case k == colAuthor && p.Width > 0:
			// A width kept from before the column had its least.
			cw.w[k] = max(p.Width, histColumnMin[k])
		default:
			cw.w[k] = histColumnWidth[k]
		}
	}
	cw.subj = clamp(hv.msgWant, historyMessageMin, historyMessageWant)
	// msg is the width the message keeps, 0 for it to fill the rows.
	msg := a.Settings.HistoryMessage
	d := &hv.colDrag
	dragged := histColumn(-1)
	if d.active {
		// The column dragged follows the pointer, down to its least
		// width: the graph into the
		// room the message has over the room it would like, the others
		// into the room at the end, which they leave as they narrow, the
		// message staying as wide as it was.
		dragged = d.col
		switch k := d.col; k {
		case colGraph:
			least := graphLeast(snap)
			if d.w0 < least {
				least = graphThin(snap) // thin, drawn out
			}
			cw.w[k] = clamp(d.raw, least, min(historyGraphMax, d.w0+max(0, d.room0)))
		case colMessage:
			msg = clamp(d.raw, historyMessageMin, d.w0+d.end0)
		default:
			most := d.w0 + d.end0
			if k != colAuthor {
				most = min(most, histColumnWidth[k])
			}
			cw.w[k] = clamp(d.raw, histColumnMin[k], max(histColumnMin[k], most))
			msg = d.msg0
		}
	}
	// want is the room the message would like.
	want := cw.subj
	if msg > 0 {
		want = min(want, msg)
	}
	handles := a.columnHandles(hv)
	cw.msg, cw.room = want, 1e6
	if hv.roomW() == 0 {
		return cw
	}
	room := hv.roomW() - 2*insetX - spaceM - want
	for k := range histColumns {
		room -= cw.w[k] + columnGap(k, cw.w[k], handles[k])
	}
	// Short of room, the columns give way first, each whole: a column
	// shows its values in full or not at all. Then the graph goes thin.
	// The hash, which takes little and is what a commit is looked up and
	// copied by, as beside its patch, stays while the message can narrow
	// to keep it, down to its least.
	shut := func(k histColumn) {
		if k != dragged && cw.w[k] > 0 {
			room += cw.w[k] + columnGap(k, cw.w[k], handles[k]) - columnGap(k, 0, handles[k])
			cw.w[k] = 0
			cw.gaveWay = true
		}
	}
	for _, k := range [...]histColumn{colAuthor, colDate} {
		if room < 0 {
			shut(k)
		}
	}
	if thin := graphThin(snap); room < 0 && dragged != colGraph && cw.w[colGraph] > thin {
		room += cw.w[colGraph] - thin
		cw.w[colGraph] = thin
		cw.gaveWay = true
	}
	if room+want-historyMessageMin < 0 {
		shut(colHash)
	}
	// The message takes the room, up to the width it keeps; the rest is
	// free at the end. With a column given way there is no room to leave
	// free, and the message takes it all, but under the pointer.
	cw.room = room
	cw.msg = want + room
	if msg > 0 && (!cw.gaveWay || dragged == colMessage) {
		cw.msg = min(msg, cw.msg)
	}
	cw.end = want + room - cw.msg
	return cw
}

func clamp(v, lo, hi float32) float32 { return max(lo, min(v, hi)) }

func histColumnName(k histColumn) string {
	switch k {
	case colGraph:
		return locales.HistoryColumnGraph()
	case colAuthor:
		return locales.HistoryColumnAuthor()
	case colDate:
		return locales.HistoryColumnDate()
	}
	return locales.HistoryColumnCommit()
}

// columnTitle heads a column as a Table's headers do, for the history's to
// look as the other lists'.
func columnTitle(c *ui.Context, title string) ui.Element {
	return ui.Text(c, title).FontSize(textSmall).FontWeight(600).TextColor(c.Theme().TextMuted).SingleLine()
}

// columnDivider resizes the column on its left, k, when dragged; a thin
// graph's, clicked, expands it. The author's, double-clicked, puts it back
// at its own width, and the message's gives the message the room at the
// end. A column the user shut has instead a thin column in its place.
func (a *App) columnDivider(c *ui.Context, hv *historyState, k histColumn) ui.Element {
	th := c.Theme()
	var p settings.Column
	if k != colMessage {
		p = a.Settings.HistoryColumns[histColumnKeys[k]]
	}
	if p.Hidden {
		return a.shutColumn(c, k)
	}
	d := &hv.colDrag
	g := ui.Box(c).FillHeight().Cursor(ui.CursorResizeEW).Role(ui.RoleNone)
	dx, _, held := g.Dragged()
	switch {
	case g.PressCanceled() && d.active && d.col == k:
		// The system took the press: the column goes back as it was.
		*d = colDrag{}
	case held && (!d.active || d.col != k):
		to := hv.cols.to
		*d = colDrag{active: true, col: k, msg0: to.msg, end0: to.end, room0: to.room}
		if k == colMessage {
			d.w0 = to.msg
		} else {
			d.w0 = to.w[k]
		}
		d.raw = d.w0
	case !held && d.active && d.col == k:
		d.done = true
	}
	if held {
		d.raw += dx
		// A click that slips a little is still a click.
		d.moved = d.moved || abs(d.raw-d.w0) >= dragSlop
	}
	switch {
	case k == colGraph && p.Thin:
		// The click that ends a drag is the drag's.
		if g.Clicked() && !(d.active && d.col == k && d.moved) {
			a.toggleThinGraph()
		}
	case k == colMessage && g.DoubleClicked():
		a.Settings.HistoryMessage = 0
		a.keepSettings()
	case k == colAuthor && g.DoubleClicked():
		p.Width = 0
		a.setHistoryColumn(k, p)
	}
	on := g.Hovered() || held
	g.Draw(func(p *ui.Painter, r ui.Rect) {
		col := th.Border
		switch {
		case on:
			col = th.Accent
		case !hv.cols.ruled(k):
			return
		}
		dividerLine(p, r, col)
	})
	return g
}

// ruled reports whether the divider after column k shows its line while
// the pointer is away: one between two titles does. The graph's, before
// a column with no title, and the last, with no column after it, would
// stand alone.
func (l *histLayout) ruled(k histColumn) bool {
	from := k + 1
	switch k {
	case colGraph:
		return false
	case colMessage:
		from = colAuthor
	}
	for j := from; j < histColumns; j++ {
		if l.shown[j] || l.handle[j] {
			return true
		}
	}
	return false
}

// dragSlop is how far a divider's press moves before it is a drag.
const dragSlop = 3

// shutWidth is how wide a column the user shut stands, between the
// divider before it and its own.
const shutWidth = spaceM

// shutColumn stands for column k, shut by the user: a column folded to
// shutWidth between the divider before it and its own, that a click opens
// again. Its hover box fills the space between the two lines, as a
// title's does.
func (a *App) shutColumn(c *ui.Context, k histColumn) ui.Element {
	th := c.Theme()
	g := ui.Box(c).FillHeight().Role(ui.RoleButton).Label(locales.HistoryColumnShow(histColumnName(k)))
	if g.Clicked() {
		a.showColumn(k, true)
	}
	on := g.Hovered()
	g.Draw(func(p *ui.Painter, r ui.Rect) {
		if on {
			side := float32(hoverSide)
			hoverBox(p, ui.Rect{X: r.X - side, Y: r.Y, W: shutWidth + 2*side, H: r.H}, th.SurfaceHover)
		}
		dividerLine(p, ui.Rect{X: r.X + shutWidth, Y: r.Y, W: spaceM, H: r.H}, th.Border)
	})
	return g
}

// dividerLine is a divider's line, down the middle of r, on whole
// pixels so that every divider is as sharp as the others.
func dividerLine(p *ui.Painter, r ui.Rect, col ui.Color) {
	p.Fill(snapRect(p, ui.Rect{X: r.X + r.W/2 - 0.5, Y: r.Y + r.H/4, W: 1, H: r.H / 2}), col, 0)
}

// hoverSide is how far a hover box reaches into the gaps at its sides,
// clear of the divider lines there.
const hoverSide = 2

// hoverBox is the box behind a header's title or thin column as the
// pointer is over it, centred down r and short of the header's height.
func hoverBox(p *ui.Painter, r ui.Rect, col ui.Color) {
	const h = rowHeight - spaceXS
	p.Fill(snapRect(p, ui.Rect{X: r.X, Y: r.Y + r.H/2 - h/2, W: r.W, H: h}), col, 4)
}

// snapRect moves r's edges to the display's pixels.
func snapRect(p *ui.Painter, r ui.Rect) ui.Rect {
	s := p.Scale()
	at := func(v float32) float32 { return float32(math.Round(float64(v*s))) / s }
	x, y := at(r.X), at(r.Y)
	return ui.Rect{X: x, Y: y, W: max(1/s, at(r.X+r.W)-x), H: max(1/s, at(r.Y+r.H)-y)}
}

// showColumn shows column k or shuts it, from the header's menu, its
// title or its divider. Shown, a column takes the room at the end first, then
// the message's; shut, it gives its room to the message, or to the end
// when the message keeps a width.
func (a *App) showColumn(k histColumn, show bool) {
	p := a.Settings.HistoryColumns[histColumnKeys[k]]
	p.Hidden = !show
	a.setHistoryColumn(k, p)
}

// collapseColumn shuts column k, or collapses the graph or expands it,
// as its title is clicked.
func (a *App) collapseColumn(k histColumn) {
	if k == colGraph {
		a.toggleThinGraph()
		return
	}
	a.showColumn(k, false)
}

// toggleThinGraph collapses the graph to its thin form, or expands it.
func (a *App) toggleThinGraph() {
	p := a.Settings.HistoryColumns[histColumnKeys[colGraph]]
	p.Thin = !p.Thin
	a.setHistoryColumn(colGraph, p)
}

// setHistoryColumn keeps how the user left column k.
func (a *App) setHistoryColumn(k histColumn, p settings.Column) {
	// A copy: the other windows hold the map they were given.
	cols := maps.Clone(a.Settings.HistoryColumns)
	if cols == nil {
		cols = map[string]settings.Column{}
	}
	if p == (settings.Column{}) {
		delete(cols, histColumnKeys[k])
	} else {
		cols[histColumnKeys[k]] = p
	}
	if len(cols) == 0 {
		cols = nil
	}
	a.Settings.HistoryColumns = cols
	a.keepSettings()
}

func abs(v float32) float32 { return max(v, -v) }
