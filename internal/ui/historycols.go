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
// The message is what a row is read by: when the list is too narrow for
// it, the columns at the end shut first, the hash, then the date, then
// the author, and open again as the list widens.

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

// The columns' own widths, the graph's fitting its lanes, and the least
// they settle at: the date's and the hash's show them whole.
var (
	histColumnWidth = [histColumns]float32{0, 120, 112, 64}
	histColumnMin   = [histColumns]float32{graph.DefaultStyle.LaneWidth, 80, 112, 64}
)

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
}

// gap is the space before a column w wide, closing with it.
func (l *histLayout) gap(k histColumn) float32 { return min(spaceM, l.w[k]) }

// fade is how opaque column k is: it fades under its least width.
func (l *histLayout) fade(k histColumn) float32 {
	return min(1, l.w[k]/histColumnMin[k])
}

// colDrag is a divider of the header being dragged.
type colDrag struct {
	active bool
	col    histColumn
	// raw is the width the pointer asks for, as far as it went.
	raw float32
	// done is set as the pointer lets go.
	done bool
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
	for k := range histColumns {
		built[k] = to[k] >= 0.5 || hv.cols.shown[k] || hv.colDrag.active && hv.colDrag.col == k
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
			dividers[k].Width(l.gap(k)).Hidden(!l.shown[k])
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
		if d.raw < histColumnMin[k]/2 {
			p.Hidden = true // the width it had stays, for when it is back
		} else {
			p.Width = max(histColumnMin[k], want[k])
		}
		*d = colDrag{}
		a.setHistoryColumn(k, p)
		want = a.columnWidths(hv, snap)
	}
	var l histLayout
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

// columnWidths are the widths the columns go to: those the user gave
// them or their own, the dragged one's under the pointer, and those at
// the end given way where the list is too narrow for the message.
func (a *App) columnWidths(hv *historyState, snap *lanes.Snapshot) [histColumns]float32 {
	var want [histColumns]float32
	for k := range histColumns {
		p := a.Settings.HistoryColumns[histColumnKeys[k]]
		switch {
		case p.Hidden:
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
		want[dragged] = max(0, d.raw)
		if dragged == colGraph {
			want[dragged] = min(want[dragged], historyGraphMax)
		}
	}
	if hv.rowW == 0 {
		return want
	}
	// The room the columns share, the message keeping its least.
	room := hv.rowW - 2*insetX - historyMessageMin
	for k := range histColumns {
		if k != dragged {
			room -= columnRoom(want[k])
		}
	}
	// A column widened by hand takes the message's room alone.
	if dragged >= 0 {
		want[dragged] = min(want[dragged], columnWidth(room))
		room -= columnRoom(want[dragged])
	}
	// Short of room, the columns at the end give way first, each whole:
	// a column shows its values in full or not at all.
	for k := colHash; k > colGraph && room < 0; k-- {
		if k != dragged {
			room += columnRoom(want[k])
			want[k] = 0
		}
	}
	return want
}

// columnRoom is the room a column w wide takes with the gap before it;
// columnWidth the width of one taking room r.
func columnRoom(w float32) float32 { return w + min(spaceM, w) }

func columnWidth(r float32) float32 {
	if r >= 2*spaceM {
		return r - spaceM
	}
	return max(0, r/2)
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

// columnDivider resizes column k: dragged, or double-clicked back to its
// own width.
func (a *App) columnDivider(c *ui.Context, hv *historyState, k histColumn) ui.Element {
	th := c.Theme()
	d := &hv.colDrag
	g := ui.Box(c).FillHeight().Cursor(ui.CursorResizeEW).Role(ui.RoleNone)
	if g.DoubleClicked() {
		p := a.Settings.HistoryColumns[histColumnKeys[k]]
		p.Width = 0
		a.setHistoryColumn(k, p)
	}
	dx, _, held := g.Dragged()
	switch {
	case held && (!d.active || d.col != k):
		*d = colDrag{active: true, col: k, raw: hv.cols.w[k]}
	case !held && d.active && d.col == k:
		d.done = true
	}
	if held {
		if k == colGraph {
			d.raw += dx
		} else {
			// The divider is on the column's left: leftward widens it.
			d.raw -= dx
		}
	}
	on := g.Hovered() || held
	g.Draw(func(p *ui.Painter, r ui.Rect) {
		col := th.Border
		if on {
			col = th.Accent
		}
		p.Fill(ui.Rect{X: r.X + r.W/2 - 0.5, Y: r.Y + r.H/4, W: 1, H: r.H / 2}, col, 0)
	})
	return g
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
