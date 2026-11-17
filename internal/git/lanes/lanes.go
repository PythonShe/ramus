// Package lanes lays out a commit graph in columns for drawing beside a
// history list, one row per commit.
//
// The layout is incremental and append-only: rows are added in display
// order (every commit after all of its children), and a row never changes
// once added. A lane keeps its column from where it starts to where it
// ends, and a freed column is reused by the next lane that needs one, so
// the graph is as wide as the most branches alive at once.
//
// Wide histories are the norm, not the exception: the Linux kernel has
// some 800 lanes alive at once after a merge window. So a row stores only
// what changes at it (the lanes that end in its node and the lanes its
// parents go down), and the full set of lanes is kept every checkEvery
// rows; a row in between is found by replaying from the checkpoint before
// it. Storage is in fixed-size chunks that are never moved, so a Snapshot
// taken by one goroutine reads rows while another appends.
package lanes

import (
	"slices"
	"sort"
)

// MaxColumns bounds the width of the layout. A commit that would need a
// column past it shares the last one; nothing that wide is readable anyway.
const MaxColumns = 1 << 14

const (
	free       = ^uint32(0)
	checkEvery = 64
	rowChunk   = 1 << 12
	edgeChunk  = 1 << 16
	stateChunk = 1 << 16
)

// Edge is a line in the half of a row above or below the node: from a
// column at one end to a column at the other. Color is the lane's color
// index, stable for a branch along its first-parent line.
type Edge struct {
	From, To uint16
	Color    uint16
}

type rec struct {
	commit uint32
	node   uint16
	color  uint16
	top    uint16 // lanes other than the node's that end in the node
	bottom uint16 // edges from the node to its parents
	edges  uint32 // chunk<<16 | offset of the row's first event
	merges uint8  // parents past the first, capped at 255
}

// checkpoint locates the lane colors before a row: a chunk of states, the
// offset in it and the number of columns. A color is stored plus one; 0 is
// a column with no lane.
type checkpoint struct {
	chunk, off, n uint32
}

type store struct {
	rows   [][]rec
	edges  [][]Edge
	states [][]uint16
	checks []checkpoint
	n      int
	width  int
}

// Layout is the writer's side: it appends rows. It is not safe for use by
// several goroutines; take a Snapshot for each reader.
type Layout struct {
	s store
	// lanes holds, per column, the commit that the lane in it waits for.
	lanes      []uint32
	laneColors []uint16
	nextColor  uint16
	scratch    []Edge
}

// Len returns the number of rows.
func (l *Layout) Len() int { return l.s.n }

// Width returns the number of columns any row uses.
func (l *Layout) Width() int { return l.s.width }

// Bytes returns the memory the layout holds, for budgets.
func (l *Layout) Bytes() int {
	b := len(l.s.rows)*rowChunk*20 + len(l.s.edges)*edgeChunk*6 + cap(l.s.checks)*12
	for _, c := range l.s.states {
		b += cap(c) * 2
	}
	return b + cap(l.lanes)*4 + cap(l.laneColors)*2
}

// Snapshot returns a reader of the rows appended so far. It stays valid,
// and unchanged, while the Layout goes on appending.
func (l *Layout) Snapshot() *Snapshot {
	s := l.s
	s.rows = append([][]rec(nil), s.rows...)
	s.edges = append([][]Edge(nil), s.edges...)
	s.states = append([][]uint16(nil), s.states...)
	s.checks = s.checks[:len(s.checks):len(s.checks)]
	return &Snapshot{segs: []segment{{s: s}}, n: s.n, width: s.width, colors: int(l.nextColor), at: -1}
}

func (l *Layout) column() int {
	for i, c := range l.lanes {
		if c == free {
			return i
		}
	}
	if len(l.lanes) >= MaxColumns {
		return MaxColumns - 1
	}
	l.lanes = append(l.lanes, free)
	l.laneColors = append(l.laneColors, 0)
	return len(l.lanes) - 1
}

func (l *Layout) newColor() uint16 {
	c := l.nextColor
	l.nextColor++
	return c
}

func (l *Layout) checkpoint() {
	n := len(l.lanes)
	s := &l.s
	if k := len(s.states); k == 0 || len(s.states[k-1])+n > cap(s.states[k-1]) {
		s.states = append(s.states, make([]uint16, 0, max(stateChunk, n)))
	}
	k := len(s.states) - 1
	c := &s.states[k]
	cp := checkpoint{chunk: uint32(k), off: uint32(len(*c)), n: uint32(n)}
	for i, w := range l.lanes {
		if w == free {
			*c = append(*c, 0)
		} else {
			*c = append(*c, l.laneColors[i]+1)
		}
	}
	s.checks = append(s.checks, cp)
}

// Append adds the next row: commit is the row's commit and parents its
// parents, in order, both as ids of the caller's choosing (positions in a
// commit-graph, for instance). A commit must come after every commit that
// names it as a parent.
func (l *Layout) Append(commit uint32, parents []uint32) {
	if l.s.n%checkEvery == 0 {
		l.checkpoint()
	}
	ev := l.scratch[:0]
	// The node goes in the leftmost lane waiting for this commit, or a new
	// lane for a branch tip.
	node := -1
	for i, c := range l.lanes {
		if c == commit {
			node = i
			break
		}
	}
	if node < 0 {
		node = l.column()
		l.laneColors[node] = l.newColor()
	}
	color := l.laneColors[node]
	for i, c := range l.lanes {
		if c == commit {
			if i != node {
				ev = append(ev, Edge{From: uint16(i), To: uint16(node), Color: l.laneColors[i]})
			}
			l.lanes[i] = free
		}
	}
	top := len(ev)
	// The first parent continues the node's lane; others join a lane
	// already waiting for them, or start one.
	for k, p := range parents {
		target := -1
		if k == 0 {
			target = node
		} else {
			for i, c := range l.lanes {
				if c == p {
					target = i
					break
				}
			}
			if target < 0 {
				target = l.column()
				l.laneColors[target] = l.newColor()
			}
		}
		if l.lanes[target] == free {
			l.lanes[target] = p
		}
		ev = append(ev, Edge{From: uint16(node), To: uint16(target), Color: l.laneColors[target]})
	}
	for i := len(l.lanes) - 1; i >= 0 && l.lanes[i] == free; i-- {
		l.lanes = l.lanes[:i]
		l.laneColors = l.laneColors[:i]
	}
	l.scratch = ev

	if len(ev) > edgeChunk {
		ev = ev[:edgeChunk] // more lanes than columns: cannot happen
	}
	s := &l.s
	if k := len(s.edges); k == 0 || len(s.edges[k-1])+len(ev) > edgeChunk {
		s.edges = append(s.edges, make([]Edge, 0, edgeChunk))
	}
	k := len(s.edges) - 1
	c := &s.edges[k]
	at := uint32(k)<<16 | uint32(len(*c))
	*c = append(*c, ev...)
	if s.n%rowChunk == 0 {
		s.rows = append(s.rows, make([]rec, 0, rowChunk))
	}
	merges := min(max(len(parents)-1, 0), 255)
	r := &s.rows[len(s.rows)-1]
	*r = append(*r, rec{
		commit: commit,
		node:   uint16(node), color: color,
		top: uint16(top), bottom: uint16(len(ev) - top),
		edges: at, merges: uint8(merges),
	})
	s.n++
	s.width = max(s.width, node+1, len(l.lanes))
	for _, e := range ev {
		s.width = max(s.width, int(e.From)+1, int(e.To)+1)
	}
}

// Snapshot reads the rows of a layout as they were when it was taken. It
// keeps a cursor so that rows read in order cost one step each; it is not
// safe for use by several goroutines at once, but for Len, Width and
// Commit, which read only what never changes.
//
// It may be several layouts one above the other (Above): new rows laid
// over rows laid out before, which stay as they were.
type Snapshot struct {
	// segs are the layouts, top first.
	segs   []segment
	n      int
	width  int
	colors int
	// cur is the segment the state is in; state holds the lane colors
	// (plus one) before its row at.
	cur   int
	state []uint16
	at    int
	ends  []bool
}

// segment is one layout of a snapshot, from row first on. enter is the
// color (plus one) of a lane coming from the layout above into the node
// of its first row, in column 0; 0 for none.
type segment struct {
	s     store
	first int
	enter uint16
}

// maxSegments bounds the layouts one snapshot stacks: each costs a step
// to find a row in, and is laid out anew with the others past it.
const maxSegments = 256

// Len returns the number of rows.
func (s *Snapshot) Len() int { return s.n }

// Width returns the number of columns any row uses.
func (s *Snapshot) Width() int { return s.width }

// Commit returns the commit id that row i was appended with.
func (s *Snapshot) Commit(i int) uint32 {
	g, k := s.find(i)
	return s.segs[g].s.rec(k).commit
}

// Merges returns the number of parents past the first of row i's
// commit, as Row does, without moving the lanes there.
func (s *Snapshot) Merges(i int) int {
	g, k := s.find(i)
	return int(s.segs[g].s.rec(k).merges)
}

// find returns the segment of row i and the row's number in it.
func (s *Snapshot) find(i int) (int, int) {
	if len(s.segs) == 1 {
		return 0, i
	}
	g := sort.Search(len(s.segs), func(g int) bool { return s.segs[g].first > i }) - 1
	return g, i - s.segs[g].first
}

func (s *store) rec(i int) rec { return s.rows[i/rowChunk][i%rowChunk] }

func (s *store) events(r rec) []Edge {
	c := s.edges[r.edges>>16]
	off := r.edges & 0xffff
	n := uint32(r.top) + uint32(r.bottom)
	return c[off : off+n : off+n]
}

// seek brings the state to the lanes before row i, and returns its
// segment and its number there.
func (s *Snapshot) seek(i int) (*segment, int) {
	g, k := s.find(i)
	seg := &s.segs[g]
	if s.at < 0 || g != s.cur || k < s.at || k-s.at > checkEvery {
		cp := seg.s.checks[k/checkEvery]
		s.state = append(s.state[:0], seg.s.states[cp.chunk][cp.off:cp.off+cp.n]...)
		s.cur, s.at = g, k/checkEvery*checkEvery
		if s.at == 0 && seg.enter != 0 {
			s.grow(0)
			s.state[0] = seg.enter
		}
	}
	for s.at < k {
		s.step(&seg.s, seg.s.rec(s.at))
		s.at++
	}
	return seg, k
}

func (s *Snapshot) grow(col int) {
	for len(s.state) <= col {
		s.state = append(s.state, 0)
	}
}

func (s *Snapshot) step(st *store, r rec) {
	ev := st.events(r)
	s.grow(int(r.node))
	s.state[r.node] = 0
	for _, e := range ev[:r.top] {
		s.state[e.From] = 0
	}
	for _, e := range ev[r.top:] {
		s.grow(int(e.To))
		s.state[e.To] = e.Color + 1
	}
}

// Above returns the rows of l laid over those of base: l's first, then
// base's as they are, as if laid out together from the top. That holds
// when every lane l leaves open ends in base's first row: l is commits
// added on top of base's newest, and nothing else of base moves. ok is
// false otherwise, or when base stacks too many layouts already; then the
// whole needs laying out anew.
//
// The lane going down into base takes the color its first row has there,
// and l's other lanes colors base has not used, so base's rows keep
// theirs. l is not to be used afterwards.
func (l *Layout) Above(base *Snapshot) (_ *Snapshot, ok bool) {
	if base.Len() == 0 {
		if len(l.lanes) != 0 {
			return nil, false
		}
		return l.Snapshot(), true
	}
	if len(l.lanes) != 1 || l.lanes[0] != base.Commit(0) || len(base.segs) >= maxSegments {
		return nil, false
	}
	down := base.segs[0].s.rec(0).color
	join, offset := l.laneColors[0], uint16(base.colors)
	color := func(c uint16) uint16 {
		if c == join {
			return down
		}
		return offset + c
	}
	// Exactly as long as it needs: a commit at a time, a layout each.
	s := &l.s
	for _, rows := range s.rows {
		for k := range rows {
			rows[k].color = color(rows[k].color)
		}
	}
	for _, es := range s.edges {
		for k := range es {
			es[k].Color = color(es[k].Color)
		}
	}
	for _, st := range s.states {
		for k, c := range st {
			if c != 0 {
				st[k] = color(c-1) + 1
			}
		}
	}
	top := segment{s: store{
		rows: compact(s.rows), edges: compact(s.edges), states: compact(s.states),
		checks: slices.Clip(s.checks), n: s.n, width: s.width,
	}}
	segs := make([]segment, 0, len(base.segs)+1)
	segs = append(segs, top)
	for k, g := range base.segs {
		g.first += s.n
		if k == 0 {
			g.enter = down + 1
		}
		segs = append(segs, g)
	}
	return &Snapshot{segs: segs, n: s.n + base.n, width: max(s.width, base.width), colors: base.colors + int(l.nextColor), at: -1}, true
}

// compact copies chunks to slices of their length alone.
func compact[T any](chunks [][]T) [][]T {
	out := make([][]T, len(chunks))
	for i, c := range chunks {
		out[i] = slices.Clone(c)
	}
	return out
}

// Row describes one row for drawing.
type Row struct {
	Node  int    // the node's column
	Color uint16 // the node's color index
	// Merges is the number of parents past the first: 0 for an ordinary
	// commit.
	Merges int
	// Top are the lines from the top of the row to its middle, Bottom from
	// the middle to the bottom.
	Top, Bottom []Edge
	// Hidden counts the lanes running through the row past maxCols, left
	// out of it.
	Hidden int
}

// Row returns row i, with edges only in columns below maxCols (0 for all
// of them), so that drawing a narrow graph of a wide history costs what
// shows. Edges are appended to buf, which it reuses, so drawing allocates
// nothing once warm.
func (s *Snapshot) Row(i, maxCols int, buf []Edge) (Row, []Edge) {
	if maxCols <= 0 {
		maxCols = MaxColumns
	}
	seg, k := s.seek(i)
	r := seg.s.rec(k)
	ev := seg.s.events(r)
	buf = buf[:0]
	hidden := 0
	if len(s.ends) < len(s.state) {
		s.ends = make([]bool, len(s.state))
	}
	for _, e := range ev[:r.top] {
		s.ends[e.From] = true
	}
	for col, c := range s.state {
		if c != 0 && col >= maxCols {
			hidden++
		}
		if c == 0 || col >= maxCols {
			continue
		}
		to := uint16(col)
		if col == int(r.node) || s.ends[col] {
			to = r.node
		}
		buf = append(buf, Edge{From: uint16(col), To: to, Color: c - 1})
	}
	top := len(buf)
	for col, c := range s.state {
		if c == 0 || col >= maxCols || col == int(r.node) || s.ends[col] {
			continue
		}
		buf = append(buf, Edge{From: uint16(col), To: uint16(col), Color: c - 1})
	}
	for _, e := range ev[:r.top] {
		s.ends[e.From] = false
	}
	for _, e := range ev[r.top:] {
		if int(e.From) < maxCols || int(e.To) < maxCols {
			buf = append(buf, e)
		}
	}
	return Row{
		Node:   int(r.node),
		Color:  r.color,
		Merges: int(r.merges),
		Top:    buf[:top:top],
		Bottom: buf[top:],
		Hidden: hidden,
	}, buf
}
