// Package graph paints the commit graph column of a history row from the
// engine's lane layout.
package graph

import (
	"github.com/egoist/mygo/ui"

	"ramus/internal/git/lanes"
)

// Style sets the geometry of the graph column.
type Style struct {
	LaneWidth float32 // distance between columns
	Line      float32 // line width
	Node      float32 // node radius
}

// DefaultStyle suits a row about 24 DIPs high.
var DefaultStyle = Style{LaneWidth: 12, Line: 1.5, Node: 3.5}

// The lanes' colors by their color index, one set for each theme: those
// bright enough on a dark background fade on a light one, the amber and
// the greens under 2.5:1, and the deeper ones a light background takes
// are lost on a dark one.
var (
	darkLanes = [...]ui.Color{
		ui.Hex("#3b82f6"), ui.Hex("#ef4444"), ui.Hex("#10b981"), ui.Hex("#f59e0b"),
		ui.Hex("#8b5cf6"), ui.Hex("#ec4899"), ui.Hex("#14b8a6"), ui.Hex("#f97316"),
	}
	lightLanes = [...]ui.Color{
		ui.Hex("#2563eb"), ui.Hex("#dc2626"), ui.Hex("#047857"), ui.Hex("#b45309"),
		ui.Hex("#7c3aed"), ui.Hex("#be185d"), ui.Hex("#0f766e"), ui.Hex("#c2410c"),
	}
)

// Ink is what a row is painted in: the lanes' colors for the theme, and
// Cut for the mark of lanes left out. A chosen row sets Chosen, for every
// line and node to take Solid: on the accent, which one lane or another
// shares, the lanes' own colors vanish or clash.
type Ink struct {
	Dark   bool
	Cut    ui.Color
	Chosen bool
	Solid  ui.Color
}

// Color returns the color of a lane's color index.
func (k Ink) Color(i uint16) ui.Color {
	switch {
	case k.Chosen:
		return k.Solid
	case k.Dark:
		return darkLanes[int(i)%len(darkLanes)]
	}
	return lightLanes[int(i)%len(lightLanes)]
}

// Columns returns how many columns fit in width.
// MinLaneWidth is the closest Fit puts the lanes.
const MinLaneWidth = 4

// Fit returns s with lanes lanes fitted to width: closer together, down to
// MinLaneWidth, and their lines and nodes thinner as they close up, so a
// narrow graph shows the whole history's shape rather than cutting it.
func (s Style) Fit(width float32, lanes int) Style {
	gap := min(s.LaneWidth, max(MinLaneWidth, width/float32(max(1, lanes))))
	t := (gap - MinLaneWidth) / (s.LaneWidth - MinLaneWidth)
	return Style{LaneWidth: gap, Line: 1 + (s.Line-1)*t, Node: 2.5 + (s.Node-2.5)*t}
}

func (s Style) Columns(width float32) int {
	// A hair over, for a width Fit divided to come back whole.
	return max(1, int(width/s.LaneWidth+1e-3))
}

// Paint draws one row's lines and node into r in the colors of k. Edges
// past the columns that fit are left out; a node past them is drawn at
// the edge, hollow, and rows with lanes left out show a dotted edge.
func Paint(p *ui.Painter, r ui.Rect, row lanes.Row, s Style, k Ink, path *ui.Path) {
	cols := s.Columns(r.W)
	if row.Hidden > 0 {
		ex := r.X + s.LaneWidth*float32(cols) - s.Line
		for dot := range 3 {
			y := r.Y + r.H*(float32(dot)+0.5)/3
			p.Line(ex, y-1, ex, y+1, s.Line, k.Cut)
		}
	}
	x := func(col uint16) float32 {
		c := min(int(col), cols-1)
		return r.X + s.LaneWidth*(float32(c)+0.5)
	}
	mid := r.Y + r.H/2
	edge := func(e lanes.Edge, y0, y1 float32) {
		x0, x1 := x(e.From), x(e.To)
		col := k.Color(e.Color)
		if x0 == x1 {
			p.Line(x0, y0, x0, y1, s.Line, col)
			return
		}
		// A curve that leaves and arrives vertically, as a lane bends into
		// its neighbour.
		path.Reset()
		path.MoveTo(x0, y0)
		path.QuadTo(x0, (y0+y1)/2, (x0+x1)/2, (y0+y1)/2)
		path.QuadTo(x1, (y0+y1)/2, x1, y1)
		p.StrokePath(path, s.Line, col)
	}
	for _, e := range row.Top {
		edge(e, r.Y, mid)
	}
	for _, e := range row.Bottom {
		edge(e, mid, r.Y+r.H)
	}
	cx := x(uint16(row.Node))
	col := k.Color(row.Color)
	path.Reset()
	if row.Node >= cols || row.Merges > 0 {
		// A merge reads as a ring, so it is told apart without color: a
		// stroke, whose hole shows the row as it is, hovered, chosen or
		// not, where a disc of the background showed through.
		path.Circle(cx, mid, s.Node-s.Line/2)
		p.StrokePath(path, s.Line, col)
		return
	}
	path.Circle(cx, mid, s.Node)
	p.FillPath(path, col)
}
