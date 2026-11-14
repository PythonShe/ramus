package ui

import (
	"maps"
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
// after it along, leaving that room free. The message's divider is how
// room is made, down to the message's least width; a double click on it
// gives the message all of it back.
//
// The date and the hash always take as long, so they keep their widths:
// the hash has no divider, and the date's can only shut it.
//
// A column dragged left follows the pointer down to nothing, fading as it
// goes under its least width; let go, it settles as a magnet would: shut
// when under half its least width, else at its least at the narrowest,
// so that at rest none shows part of its values. A shut column keeps its
// divider, to drag it out again into the room at the end, and the
// header's menu brings it back too.
//
// The graph does not shut but collapses to a thin form, its lanes at
// their closest, which still shows where history branches and merges:
// dragged most of the way to it, or by a click on its divider, which
// expands it again. Narrower than its lanes need, the graph draws them
// closer together rather than cutting them off.
//
// The message is what a row is read by. It would like the room its
// subjects take, up to historyMessageWant: when the list is too narrow
// for that, the free room at the end goes first, then the columns at the
// end shut, the hash, then the date, then the author, and then the graph
// goes thin; they come back as the list widens. Only then does the
// message narrow, down to historyMessageMin. Nor does a drag widen a
// column into the room the message would like.

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
// the hash's showing them whole; the graph's are graphWidth and
// graphLeast.
var (
	histColumnWidth = [histColumns]float32{0, 120, 112, 64}
	histColumnMin   = [histColumns]float32{0, 80, 112, 64}
)

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

// colWant is where the columns go: their widths and the free room at
// the end; msg is the room the message would like, room what it has over
// that, and spare what it had before any column gave way.
type colWant struct {
	w                     [histColumns]float32
	end, msg, room, spare float32
}

// graphStyle draws the graph as wide as it is.
func (l *histLayout) graphStyle() graph.Style {
	return graph.DefaultStyle.Fit(l.w[colGraph], l.lanes)
}

// gap is the space after column k: its divider's, which closes with
// the column but for a handle. The hash, the last, has none.
func (l *histLayout) gap(k histColumn) float32 { return columnGap(k, l.w[k], l.handle[k]) }

func columnGap(k histColumn, w float32, handle bool) float32 {
	switch {
	case k == colHash:
		return 0
	case handle:
		return spaceM
	}
	return min(spaceM, w)
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
	// column's as the press began, and end0, room0 and spare0 the
	// layout's end, room and spare then (colWant).
	raw, w0, end0, room0, spare0 float32
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
	head := ui.Row(c).Key("columns").Height(rowHeight).Padding(0, insetX).AlignItems(ui.Center).FillWidth()
	head.Children(func() {
		if built[colGraph] {
			titles[colGraph] = columnTitle(c, "")
			dividers[colGraph] = a.columnDivider(c, hv, colGraph)
		}
		ui.Text(c, locales.HistoryColumnMessage()).FontSize(textCaption).TextColor(th.TextMuted).SingleLine().Grow(1).Shrink(1)
		a.columnDivider(c, hv, colMessage).Width(spaceM)
		for k := colAuthor; k < histColumns; k++ {
			if built[k] {
				titles[k] = columnTitle(c, histColumnName(k))
				if k != colHash {
					dividers[k] = a.columnDivider(c, hv, k)
				}
			}
		}
	})
	l := a.historyColumns(head, hv, snap)
	hv.cols = l
	head.PaddingEnd(insetX + l.end)
	for k := range histColumns {
		if built[k] {
			titles[k].Width(l.w[k]).Opacity(l.fade(k)).Hidden(!l.shown[k])
			if k != colHash {
				dividers[k].Width(l.gap(k)).Hidden(!l.shown[k] && !l.handle[k])
			}
		}
	}
	head.ContextMenu(func(m *ui.Menu) {
		for k := range histColumns {
			p := a.Settings.HistoryColumns[histColumnKeys[k]]
			if m.Item(histColumnName(k)).Checked(!p.Hidden).Chosen() {
				a.showColumn(hv, k, p.Hidden)
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
		if m.Item(locales.HistoryColumnsReset()).Disabled(len(a.Settings.HistoryColumns) == 0 && a.Settings.HistoryEnd == 0).Chosen() {
			a.Settings.HistoryColumns, a.Settings.HistoryEnd = nil, 0
			a.keepSettings()
		}
	})
}

// historyColumns lays the columns out for the list as wide as it was in
// the last frame, their widths animated on head.
func (a *App) historyColumns(head ui.Element, hv *historyState, snap *lanes.Snapshot) histLayout {
	d := &hv.colDrag
	want := a.columnWidths(hv, snap)
	if d.active && d.done {
		a.letGo(d, want, snap)
		*d = colDrag{}
		want = a.columnWidths(hv, snap)
	}
	l := histLayout{least: histColumnMin, lanes: snap.Width(), handle: a.columnHandles(hv), to: want}
	l.least[colGraph] = graphThin(snap)
	for k := range histColumns {
		if d.active && d.col == k {
			l.w[k] = head.Jump(k, want.w[k])
		} else {
			l.w[k] = head.AnimateTo(k, want.w[k], columnSettle)
		}
		// Under half a point a column is gone; the one dragged stays,
		// for its divider to keep the pointer.
		l.shown[k] = l.w[k] >= 0.5 || d.active && d.col == k
	}
	if d.active && d.col != colGraph {
		l.end = head.Jump("end", want.end)
	} else {
		l.end = head.AnimateTo("end", want.end, columnSettle)
	}
	return l
}

// letGo settles the column dragged as the pointer lets go, where the
// pointer left it: want.
func (a *App) letGo(d *colDrag, want colWant, snap *lanes.Snapshot) {
	if !d.moved {
		return // a click, which the divider took
	}
	k := d.col
	if k == colMessage {
		a.Settings.HistoryEnd = want.end
		a.keepSettings()
		return
	}
	p := a.Settings.HistoryColumns[histColumnKeys[k]]
	end := a.Settings.HistoryEnd
	switch least := histColumnMin[k]; {
	case k == colGraph:
		// Thin when let go nearer its thin form than the full graph's
		// least; the width it had stays, for when it expands.
		thin, least := graphThin(snap), graphLeast(snap)
		p.Hidden, p.Thin = false, d.raw < (thin+least)/2
		if !p.Thin {
			p.Width = max(least, want.w[k])
		}
	case want.w[k] < least/2 || d.w0+d.end0 < least:
		// Shut, or kept shut when there is no room to open it whole;
		// the width it had stays, for when it is back.
		p.Hidden = p.Hidden || d.w0 > 0
		end = d.end0 + d.w0
	default:
		w := max(least, want.w[k])
		p.Hidden = false
		if k == colAuthor {
			p.Width = w
		}
		end = d.end0 + d.w0 - w
	}
	a.Settings.HistoryEnd = max(0, end)
	a.setHistoryColumn(k, p)
}

// columnHandles are the columns whose divider stays as they shut: those
// the user shut and the one being dragged. The hash has no divider.
func (a *App) columnHandles(hv *historyState) (handles [histColumns]bool) {
	for k := range colHash {
		handles[k] = a.Settings.HistoryColumns[histColumnKeys[k]].Hidden || hv.colDrag.active && hv.colDrag.col == k
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
			cw.w[k] = p.Width
		default:
			cw.w[k] = histColumnWidth[k]
		}
	}
	cw.end = max(0, a.Settings.HistoryEnd)
	d := &hv.colDrag
	dragged := histColumn(-1)
	if d.active {
		dragged = d.col
	}
	handles := a.columnHandles(hv)
	cw.msg = clamp(hv.msgWant, historyMessageMin, historyMessageWant)
	cw.room, cw.spare = 1e6, 1e6
	if hv.rowW > 0 {
		room := hv.rowW - 2*insetX - cw.msg - spaceM - cw.end
		for k := range histColumns {
			room -= cw.w[k] + columnGap(k, cw.w[k], handles[k])
		}
		// Short of room, the room at the end goes first, then the
		// columns at the end, each whole: a column shows its values in
		// full or not at all. Then the graph goes thin.
		if room < 0 {
			give := min(cw.end, -room)
			cw.end -= give
			room += give
		}
		cw.spare = max(0, room)
		for k := colHash; k > colGraph && room < 0; k-- {
			if k != dragged && cw.w[k] > 0 {
				room += cw.w[k] + columnGap(k, cw.w[k], handles[k]) - columnGap(k, 0, handles[k])
				cw.w[k] = 0
			}
		}
		if thin := graphThin(snap); room < 0 && dragged != colGraph && cw.w[colGraph] > thin {
			room += cw.w[colGraph] - thin
			cw.w[colGraph] = thin
		}
		cw.room = room
	}
	if !d.active {
		return cw
	}
	// The column dragged follows the pointer: the graph into the room
	// the message has over what it would like, the message and the others
	// into the room at the end, which they leave as they narrow. The
	// message makes no more of it than it has to spare: at rest, the
	// room at the end goes before the room the message would like.
	switch k := d.col; k {
	case colGraph:
		cw.w[k] = clamp(d.raw, 0, min(historyGraphMax, d.w0+max(0, d.room0)))
		cw.room = d.room0 + d.w0 - cw.w[k]
	case colMessage:
		cw.end = clamp(d.end0+d.w0-d.raw, 0, d.end0+d.spare0)
		cw.room = d.room0 + d.end0 - cw.end
	default:
		most := d.w0 + d.end0
		if k == colDate {
			most = min(most, histColumnWidth[colDate])
		}
		cw.w[k] = clamp(d.raw, 0, most)
		cw.end = d.end0 + d.w0 - cw.w[k]
	}
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

func columnTitle(c *ui.Context, title string) ui.Element {
	return ui.Text(c, title).FontSize(textCaption).TextColor(c.Theme().TextMuted).SingleLine()
}

// columnDivider resizes the column on its left, k, when dragged. The
// graph's, clicked, collapses the graph to its thin form or expands it;
// the author's, double-clicked, puts it back at its own width, and the
// message's gives the message the room at the end.
func (a *App) columnDivider(c *ui.Context, hv *historyState, k histColumn) ui.Element {
	th := c.Theme()
	d := &hv.colDrag
	g := ui.Box(c).FillHeight().Cursor(ui.CursorResizeEW).Role(ui.RoleNone)
	dx, _, held := g.Dragged()
	switch {
	case held && (!d.active || d.col != k):
		to := hv.cols.to
		*d = colDrag{active: true, col: k, end0: to.end, room0: to.room, spare0: to.spare}
		if k == colMessage {
			d.w0 = to.msg + to.room
		} else {
			d.w0 = to.w[k]
		}
		d.raw = d.w0
	case !held && d.active && d.col == k:
		d.done = true
	}
	if held && dx != 0 {
		d.moved = true
		d.raw += dx
	}
	switch {
	case k == colGraph:
		p := a.Settings.HistoryColumns[histColumnKeys[k]]
		// Thin by the user's choice, or for want of room, which a click
		// cannot give it.
		thin := hv.cols.to.w[k] <= hv.cols.least[k]
		label := locales.HistoryGraphCollapse()
		if thin {
			label = locales.HistoryGraphExpand()
		}
		g.Tooltip(label)
		// The click that ends a drag is the drag's.
		if g.Clicked() && !(d.active && d.col == k && d.moved) {
			switch {
			case p.Hidden:
				a.showColumn(hv, k, true)
			case p.Thin || !thin:
				a.toggleThinGraph()
			}
		}
	case k == colMessage && g.DoubleClicked():
		a.Settings.HistoryEnd = 0
		a.keepSettings()
	case k == colAuthor && !a.Settings.HistoryColumns[histColumnKeys[k]].Hidden && g.DoubleClicked():
		p := a.Settings.HistoryColumns[histColumnKeys[k]]
		w := hv.cols.to.w[k]
		a.Settings.HistoryEnd = max(0, a.Settings.HistoryEnd+w-histColumnWidth[k])
		p.Width = 0
		a.setHistoryColumn(k, p)
	}
	on := g.Hovered() || held
	g.Draw(func(p *ui.Painter, r ui.Rect) {
		col := th.Border
		switch {
		case on:
			col = th.Accent
		case k != colMessage && !hv.cols.shown[k]:
			return // a shut column's divider shows as it is pointed at
		}
		p.Fill(ui.Rect{X: r.X + r.W/2 - 0.5, Y: r.Y + r.H/4, W: 1, H: r.H / 2}, col, 0)
	})
	return g
}

// showColumn shows column k or shuts it, from the header's menu or the
// graph's divider. Shown, a column takes the room at the end first; shut,
// it leaves the room it took there, as a drag would. Its divider stays
// either way, and the graph takes and leaves the message's room.
func (a *App) showColumn(hv *historyState, k histColumn, show bool) {
	p := a.Settings.HistoryColumns[histColumnKeys[k]]
	p.Hidden = !show
	if k != colGraph {
		if show {
			w := histColumnWidth[k]
			if k == colAuthor && p.Width > 0 {
				w = p.Width
			}
			a.Settings.HistoryEnd = max(0, a.Settings.HistoryEnd-w)
		} else {
			a.Settings.HistoryEnd += hv.cols.to.w[k]
		}
	}
	a.setHistoryColumn(k, p)
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
