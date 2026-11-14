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

// The history list's columns: the graph, the message, which takes the
// room the others leave, then the author, the date and the hash. Dragging
// a divider in the header resizes the column it belongs to, the graph's
// on its right and the others' on their left, the room coming from the
// message.
//
// A dragged column follows the pointer all the way, down to nothing,
// fading as it goes under its least width; let go, it settles as a magnet
// would: shut when dragged under half its least width, else at its least
// at the narrowest, so that at rest none shows part of its values. The
// header's menu brings a shut column back.
//
// The graph does not shut but collapses to a thin form, its lanes at
// their closest, which still shows where history branches and merges:
// dragged most of the way to it, or by a click on its divider, which
// expands it again. Narrower than its lanes need, the graph draws them
// closer together rather than cutting them off.
//
// The message is what a row is read by: when the list is too narrow for
// it, the columns at the end shut first, the hash, then the date, then
// the author, and then the graph goes thin; they come back as the list
// widens. The message alone is left to narrow.

type histColumn int

const (
	colGraph histColumn = iota
	colAuthor
	colDate
	colHash
	histColumns
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

const (
	// historyMessageMin is the room the message keeps before the columns
	// at the end give way.
	historyMessageMin = 200
	// historyGraphMax bounds a graph widened by hand.
	historyGraphMax = 320
)

// columnSettle is how a column comes to rest: let go, shown, shut or
// given way. It carries on at the speed the drag left it.
var columnSettle = ui.Animation{Spring: ui.SpringOptions{Duration: 350 * time.Millisecond}}

// histLayout is where the columns are in a frame.
type histLayout struct {
	shown [histColumns]bool
	w     [histColumns]float32
	// least is each column's least width; the graph's is its thin
	// form's, under which it fades.
	least [histColumns]float32
	// lanes is how many lanes the graph has.
	lanes int
	// handle marks the columns whose divider stays as they shut: those
	// the user shut, to drag out again, and the one being dragged.
	handle [histColumns]bool
}

// graphStyle draws the graph as wide as it is.
func (l *histLayout) graphStyle() graph.Style {
	return graph.DefaultStyle.Fit(l.w[colGraph], l.lanes)
}

// gap is the space before column k: its divider's, which closes with
// the column but for a handle.
func (l *histLayout) gap(k histColumn) float32 { return columnGap(l.w[k], l.handle[k]) }

func columnGap(w float32, handle bool) float32 {
	if handle {
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
	// raw is the width the pointer asks for, as far as it went.
	raw float32
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
		built[k] = to[k] >= 0.5 || hv.cols.shown[k] || handles[k]
	}
	// No gap: the dividers stand in the rows' gaps.
	head := ui.Row(c).Key("columns").Height(rowHeight).Padding(0, insetX).AlignItems(ui.Center).FillWidth()
	head.Children(func() {
		if built[colGraph] {
			titles[colGraph] = columnTitle(c, "")
			dividers[colGraph] = a.columnDivider(c, hv, colGraph)
		}
		ui.Text(c, locales.HistoryColumnMessage()).FontSize(textCaption).TextColor(th.TextMuted).SingleLine().Grow(1).Shrink(1)
		for k := colAuthor; k < histColumns; k++ {
			if built[k] {
				dividers[k] = a.columnDivider(c, hv, k)
				titles[k] = columnTitle(c, histColumnName(k))
			}
		}
	})
	l := a.historyColumns(head, hv, snap)
	hv.cols = l
	for k := range histColumns {
		if built[k] {
			titles[k].Width(l.w[k]).Opacity(l.fade(k)).Hidden(!l.shown[k])
			dividers[k].Width(l.gap(k)).Hidden(!l.shown[k] && !l.handle[k])
		}
	}
	head.ContextMenu(func(m *ui.Menu) {
		for k := range histColumns {
			p := a.Settings.HistoryColumns[histColumnKeys[k]]
			if m.Item(histColumnName(k)).Checked(!p.Hidden).Chosen() {
				p.Hidden = !p.Hidden
				a.setHistoryColumn(k, p)
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
		if m.Item(locales.HistoryColumnsReset()).Disabled(len(a.Settings.HistoryColumns) == 0).Chosen() {
			a.Settings.HistoryColumns = nil
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
		// Let go: the column settles from where the pointer left it.
		k := d.col
		p := a.Settings.HistoryColumns[histColumnKeys[k]]
		switch least := histColumnMin[k]; {
		case !d.moved:
			// A click, which the divider took.
		case k == colGraph:
			// Thin when let go nearer its thin form than the full
			// graph's least; the width it had stays, for when it
			// expands.
			thin, least := graphThin(snap), graphLeast(snap)
			p.Hidden, p.Thin = false, d.raw < (thin+least)/2
			if !p.Thin {
				p.Width = max(least, want[k])
			}
		case d.raw < least/2:
			p.Hidden = true // the width it had stays, for when it is back
		default:
			p.Hidden, p.Width = false, max(least, want[k])
		}
		*d = colDrag{}
		a.setHistoryColumn(k, p)
		want = a.columnWidths(hv, snap)
	}
	l := histLayout{least: histColumnMin, lanes: snap.Width(), handle: a.columnHandles(hv)}
	l.least[colGraph] = graphThin(snap)
	for k := range histColumns {
		if d.active && d.col == k {
			l.w[k] = head.Jump(k, want[k])
		} else {
			l.w[k] = head.AnimateTo(k, want[k], columnSettle)
		}
		// Under half a point a column is gone; the one dragged stays,
		// for its divider to keep the pointer.
		l.shown[k] = l.w[k] >= 0.5 || d.active && d.col == k
	}
	return l
}

// columnHandles are the columns whose divider stays as they shut: those
// the user shut and the one being dragged.
func (a *App) columnHandles(hv *historyState) (handles [histColumns]bool) {
	for k := range histColumns {
		handles[k] = a.Settings.HistoryColumns[histColumnKeys[k]].Hidden || hv.colDrag.active && hv.colDrag.col == k
	}
	return handles
}

// columnWidths are the widths the columns go to: those the user gave
// them or their own, those at the end given way where the list is too
// narrow for the message, and the dragged one's under the pointer, in
// the room the others leave.
func (a *App) columnWidths(hv *historyState, snap *lanes.Snapshot) [histColumns]float32 {
	var want [histColumns]float32
	for k := range histColumns {
		p := a.Settings.HistoryColumns[histColumnKeys[k]]
		switch {
		case p.Hidden:
		case k == colGraph && p.Thin:
			want[k] = graphThin(snap)
		case p.Width > 0:
			want[k] = p.Width
		case k == colGraph:
			want[k] = graphWidth(snap)
		default:
			want[k] = histColumnWidth[k]
		}
	}
	want[colGraph] = min(want[colGraph], historyGraphMax)
	d := &hv.colDrag
	dragged := histColumn(-1)
	if d.active {
		dragged = d.col
	}
	handles := a.columnHandles(hv)
	// The room the columns leave the message over its least, as they
	// were before any drag.
	room := float32(historyGraphMax + 1e6)
	if hv.rowW > 0 {
		room = hv.rowW - 2*insetX - historyMessageMin
		for k := range histColumns {
			room -= want[k] + columnGap(want[k], handles[k])
		}
		// Short of room, the columns at the end give way first, each
		// whole: a column shows its values in full or not at all. Then
		// the graph goes thin.
		for k := colHash; k > colGraph && room < 0; k-- {
			if k != dragged && want[k] > 0 {
				room += want[k] + columnGap(want[k], handles[k]) - columnGap(0, handles[k])
				want[k] = 0
			}
		}
		if thin := graphThin(snap); room < 0 && dragged != colGraph && want[colGraph] > thin {
			room += want[colGraph] - thin
			want[colGraph] = thin
		}
	}
	// The column dragged follows the pointer, widening into the room the
	// others leave, not over them.
	if dragged >= 0 {
		w := min(max(0, d.raw), want[dragged]+max(0, room))
		if dragged == colGraph {
			w = min(w, historyGraphMax)
		}
		want[dragged] = w
	}
	return want
}

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

// columnDivider resizes column k when dragged. Double-clicked, it puts
// the column back at its own width; the graph's, clicked, collapses the
// graph to its thin form or expands it.
func (a *App) columnDivider(c *ui.Context, hv *historyState, k histColumn) ui.Element {
	th := c.Theme()
	d := &hv.colDrag
	g := ui.Box(c).FillHeight().Cursor(ui.CursorResizeEW).Role(ui.RoleNone)
	dx, _, held := g.Dragged()
	switch {
	case held && (!d.active || d.col != k):
		*d = colDrag{active: true, col: k, raw: hv.cols.w[k]}
	case !held && d.active && d.col == k:
		d.done = true
	}
	if held && dx != 0 {
		d.moved = true
		if k == colGraph {
			d.raw += dx
		} else {
			// The divider is on the column's left: leftward widens it.
			d.raw -= dx
		}
	}
	if k == colGraph {
		label := locales.HistoryGraphCollapse()
		if a.Settings.HistoryColumns[histColumnKeys[k]].Thin {
			label = locales.HistoryGraphExpand()
		}
		g.Tooltip(label)
		// The click that ends a drag is the drag's.
		if g.Clicked() && !(d.active && d.col == k && d.moved) {
			if p := a.Settings.HistoryColumns[histColumnKeys[k]]; p.Hidden {
				p.Hidden = false
				a.setHistoryColumn(k, p)
			} else {
				a.toggleThinGraph()
			}
		}
	} else if g.DoubleClicked() {
		p := a.Settings.HistoryColumns[histColumnKeys[k]]
		p.Width = 0
		a.setHistoryColumn(k, p)
	}
	on := g.Hovered() || held
	g.Draw(func(p *ui.Painter, r ui.Rect) {
		col := th.Border
		switch {
		case on:
			col = th.Accent
		case !hv.cols.shown[k]:
			return // a shut column's divider shows as it is pointed at
		}
		p.Fill(ui.Rect{X: r.X + r.W/2 - 0.5, Y: r.Y + r.H/4, W: 1, H: r.H / 2}, col, 0)
	})
	return g
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
