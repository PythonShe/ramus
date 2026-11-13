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

// palette colors lanes by their color index; chosen to stay apart in light
// and dark themes.
var palette = [...]ui.Color{
	ui.Hex("#3b82f6"), ui.Hex("#ef4444"), ui.Hex("#10b981"), ui.Hex("#f59e0b"),
	ui.Hex("#8b5cf6"), ui.Hex("#ec4899"), ui.Hex("#14b8a6"), ui.Hex("#f97316"),
}

// Color returns the color of a lane's color index.
func Color(i uint16) ui.Color { return palette[int(i)%len(palette)] }

// Columns returns how many columns fit in width.
func (s Style) Columns(width float32) int {
	return max(1, int(width/s.LaneWidth))
}

// Paint draws one row's lines and node into r; bg is the row background,
// which hollows merge nodes. Edges past the columns that
// fit are left out; a node past them is drawn at the edge, hollow.
func Paint(p *ui.Painter, r ui.Rect, row lanes.Row, s Style, bg ui.Color, path *ui.Path) {
	cols := s.Columns(r.W)
	x := func(col uint16) float32 {
		c := min(int(col), cols-1)
		return r.X + s.LaneWidth*(float32(c)+0.5)
	}
	mid := r.Y + r.H/2
	edge := func(e lanes.Edge, y0, y1 float32) {
		x0, x1 := x(e.From), x(e.To)
		col := Color(e.Color)
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
	col := Color(row.Color)
	path.Reset()
	path.Circle(cx, mid, s.Node)
	if row.Node >= cols {
		p.StrokePath(path, s.Line, col)
		return
	}
	p.FillPath(path, col)
	if row.Merges > 0 {
		// A merge reads as a ring, so it is told apart without color.
		path.Reset()
		path.Circle(cx, mid, s.Node-s.Line)
		p.FillPath(path, bg)
	}
}
