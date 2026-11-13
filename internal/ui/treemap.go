package ui

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"ramus/internal/session"
	"ramus/internal/ui/pathtree"
)

// treemapData is one treemap's files as a tree, each node weighed: a
// folder by the sum of what is in it.
type treemapData struct {
	pw      session.PathWeights
	tree    *pathtree.Tree
	weights []int64 // by node
}

func newTreemapData(pw session.PathWeights) *treemapData {
	d := &treemapData{pw: pw}
	if pw.Err != nil || len(pw.Paths) == 0 {
		return d
	}
	d.tree = pathtree.Build(pw.Paths, 0)
	d.weights = make([]int64, d.tree.Len())
	var sum func(n pathtree.Node) int64
	sum = func(n pathtree.Node) int64 {
		if !d.tree.IsDir(n) {
			d.weights[n] = pw.Weights[d.tree.Index(n)]
			return d.weights[n]
		}
		var w int64
		for _, k := range d.tree.Children(n) {
			w += sum(k)
		}
		d.weights[n] = w
		return w
	}
	sum(pathtree.Root)
	return d
}

// treemapState is where a treemap view is: the folders opened down to the
// one shown.
type treemapState struct {
	data    *treemapData
	loading bool
	// path are the folders from the root to the one shown.
	path []pathtree.Node
	// For is the request the data was read for.
	For session.StatsRequest
	// laidW and laidH are the size the tiles were last laid out for.
	laidW, laidH float32
}

// maxTiles bounds the tiles of one folder; the smallest are shown as one.
const maxTiles = 60

// tile is one rectangle: a node, or the smallest nodes as one (other > 0).
type tile struct {
	node  pathtree.Node
	w     float64
	other int
}

// strip is a row or column of tiles the squarified layout lays along the
// shorter side of the space left.
type strip struct {
	across bool // tiles side by side in a row across the top; else a column at the left
	tiles  []tile
	w      float64
}

// squarify lays tiles, largest first, in strips that keep them as close
// to square as it can (Bruls, Huizing and van Wijk), in a w by h space.
func squarify(tiles []tile, w, h float64) []strip {
	total := 0.0
	for _, t := range tiles {
		total += t.w
	}
	if total <= 0 || w <= 0 || h <= 0 {
		return nil
	}
	scale := w * h / total
	var out []strip
	for len(tiles) > 0 {
		side := min(w, h)
		worst := func(n int) float64 {
			sum, big, small := 0.0, 0.0, 1e308
			for _, t := range tiles[:n] {
				a := t.w * scale
				sum += a
				big, small = max(big, a), min(small, a)
			}
			if small <= 0 {
				return 1e308
			}
			s2 := side * side
			return max(s2*big/(sum*sum), sum*sum/(s2*small))
		}
		n := 1
		for n < len(tiles) && worst(n+1) <= worst(n) {
			n++
		}
		st := strip{across: w < h, tiles: tiles[:n]}
		for _, t := range st.tiles {
			st.w += t.w
		}
		out = append(out, st)
		if st.across {
			h -= st.w * scale / w
		} else {
			w -= st.w * scale / h
		}
		tiles = tiles[n:]
	}
	return out
}

// tilesOf returns a folder's children as tiles, largest first, the
// smallest folded into one past maxTiles.
func (d *treemapData) tilesOf(n pathtree.Node) []tile {
	var ts []tile
	for _, k := range d.tree.Children(n) {
		if d.weights[k] > 0 {
			ts = append(ts, tile{node: k, w: float64(d.weights[k])})
		}
	}
	slices.SortFunc(ts, func(a, b tile) int { return cmp.Compare(b.w, a.w) })
	if len(ts) > maxTiles {
		rest := tile{other: len(ts) - maxTiles + 1}
		for _, t := range ts[maxTiles-1:] {
			rest.w += t.w
		}
		ts = append(ts[:maxTiles-1], rest)
	}
	return ts
}

// treemapView shows the folder the state is in as a treemap; a folder's
// tile opens it.
func (a *App) treemapView(c *ui.Context, ts *treemapState, unit func(int64) string) {
	th := c.Theme()
	d := ts.data
	if d == nil || ts.loading {
		ui.Column(c).Fill().Center().Children(func() { ui.Spinner(c) })
		return
	}
	if d.pw.Err != nil {
		ui.Text(c, errText(d.pw.Err)).TextColor(th.Danger).Padding(12).Selectable()
		return
	}
	if d.tree == nil {
		ui.Column(c).Fill().Center().Children(func() { ui.Text(c, "No files").TextColor(th.TextMuted) })
		return
	}
	cur := pathtree.Root
	if len(ts.path) > 0 {
		cur = ts.path[len(ts.path)-1]
	}
	ui.Row(c).Gap(4).Padding(0, 0, 8, 0).AlignItems(ui.Center).Children(func() {
		if ui.Button(c, "All Files").Disabled(len(ts.path) == 0).Clicked() {
			ts.path = nil
		}
		for i, n := range ts.path {
			ui.Text(c, "/").TextColor(th.TextMuted)
			if ui.Button(c, d.tree.Name(n)).Disabled(i == len(ts.path)-1).Clicked() {
				ts.path = ts.path[:i+1]
			}
		}
		ui.Spacer(c)
		ui.Text(c, unit(d.weights[cur])).FontSize(12).TextColor(th.TextMuted)
	})
	tiles := d.tilesOf(cur)
	total := float64(d.weights[cur])
	box := ui.Box(c).Key("treemap").Grow(1).FillWidth().MinHeight(240)
	// Laid out at the size the box had last frame; the first frame, and
	// a resize, take one more to settle.
	b := box.LayoutBounds()
	if b.W != ts.laidW || b.H != ts.laidH {
		ts.laidW, ts.laidH = b.W, b.H
		c.AnimationFrame()
	}
	if b.W <= 0 || b.H <= 0 {
		return
	}
	w, h := float64(b.W), float64(b.H)
	box.Children(func() {
		for i, r := range place(squarify(tiles, w, h), w, h) {
			tw, th := float32(max(r.w-2, 1)), float32(max(r.h-2, 1))
			a.treemapTile(c, ts, d, r.t, total, unit, i, tw, th).
				Absolute().Left(float32(r.x)).Top(float32(r.y)).Width(tw).Height(th)
		}
	})
}

// placed is a tile and its rectangle.
type placed struct {
	t          tile
	x, y, w, h float64
}

// place turns strips into rectangles in a w by h space.
func place(strips []strip, w, h float64) []placed {
	total := 0.0
	for _, s := range strips {
		total += s.w
	}
	if total <= 0 {
		return nil
	}
	scale := w * h / total
	var out []placed
	x, y := 0.0, 0.0
	for _, s := range strips {
		area := s.w * scale
		if s.across {
			sh := area / w
			tx := x
			for _, t := range s.tiles {
				tw := t.w * scale / sh
				out = append(out, placed{t, tx, y, tw, sh})
				tx += tw
			}
			y += sh
			h -= sh
		} else {
			sw := area / h
			ty := y
			for _, t := range s.tiles {
				th := t.w * scale / sw
				out = append(out, placed{t, x, ty, sw, th})
				ty += th
			}
			x += sw
			w -= sw
		}
	}
	return out
}

func (a *App) treemapTile(c *ui.Context, ts *treemapState, d *treemapData, t tile, total float64, unit func(int64) string, key int, w, h float32) ui.Element {
	th := c.Theme()
	var name, tip string
	dir := false
	if t.other > 0 {
		name = strconv.Itoa(t.other) + " more"
		tip = fmt.Sprintf("%d smaller entries: %s", t.other, unit(int64(t.w)))
	} else {
		name = d.tree.Name(t.node)
		dir = d.tree.IsDir(t.node)
		if dir {
			name += "/"
		}
		tip = fmt.Sprintf("%s: %s, %.1f%%", name, unit(int64(t.w)), 100*t.w/total)
	}
	bg := th.Accent.Alpha(0.22)
	if dir {
		bg = th.Accent.Alpha(0.45)
	}
	el := ui.ButtonBase(c).Key(key).Background(bg).Radius(min(4, w/4, h/4)).Tooltip(tip).Label(tip).
		AlignItems(ui.Start).Justify(ui.Start)
	// A tile too small for a label shows none, so it keeps its size.
	if w >= 32 && h >= 20 {
		el.Padding(4, 6).Children(func() {
			ui.Column(c).Children(func() {
				ui.Text(c, name).FontSize(11).SingleLine()
				if h >= 40 {
					ui.Text(c, unit(int64(t.w))).FontSize(11).TextColor(th.TextMuted).SingleLine()
				}
			})
		})
	}
	if dir && el.Clicked() {
		ts.path = append(ts.path, t.node)
	}
	return el
}

// byteSize says a size in bytes the way people read it.
func byteSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return strconv.FormatInt(n, 10) + " bytes"
}

// changeCount says how many changes a path had.
func changeCount(n int64) string {
	if n == 1 {
		return "1 change"
	}
	return strings.TrimSpace(strconv.FormatInt(n, 10)) + " changes"
}
