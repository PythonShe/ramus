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
	return &Snapshot{s: s, at: -1}
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
// safe for use by several goroutines at once.
type Snapshot struct {
	s store
	// state holds the lane colors (plus one) before row at.
	state []uint16
	at    int
	ends  []bool
}

// Len returns the number of rows.
func (s *Snapshot) Len() int { return s.s.n }

// Width returns the number of columns any row uses.
func (s *Snapshot) Width() int { return s.s.width }

// Commit returns the commit id that row i was appended with.
func (s *Snapshot) Commit(i int) uint32 { return s.rec(i).commit }

func (s *Snapshot) rec(i int) rec { return s.s.rows[i/rowChunk][i%rowChunk] }

func (s *Snapshot) events(r rec) []Edge {
	c := s.s.edges[r.edges>>16]
	off := r.edges & 0xffff
	n := uint32(r.top) + uint32(r.bottom)
	return c[off : off+n : off+n]
}

// seek brings the state to the lanes before row i.
func (s *Snapshot) seek(i int) {
	if s.at < 0 || i < s.at || i-s.at > checkEvery {
		cp := s.s.checks[i/checkEvery]
		s.state = append(s.state[:0], s.s.states[cp.chunk][cp.off:cp.off+cp.n]...)
		s.at = i / checkEvery * checkEvery
	}
	for s.at < i {
		s.step(s.rec(s.at))
		s.at++
	}
}

func (s *Snapshot) grow(col int) {
	for len(s.state) <= col {
		s.state = append(s.state, 0)
	}
}

func (s *Snapshot) step(r rec) {
	ev := s.events(r)
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
}

// Row returns row i, with edges only in columns below maxCols (0 for all
// of them), so that drawing a narrow graph of a wide history costs what
// shows. Edges are appended to buf, which it reuses, so drawing allocates
// nothing once warm.
func (s *Snapshot) Row(i, maxCols int, buf []Edge) (Row, []Edge) {
	if maxCols <= 0 {
		maxCols = MaxColumns
	}
	s.seek(i)
	r := s.rec(i)
	ev := s.events(r)
	buf = buf[:0]
	if len(s.ends) < len(s.state) {
		s.ends = make([]bool, len(s.state))
	}
	for _, e := range ev[:r.top] {
		s.ends[e.From] = true
	}
	for col, c := range s.state {
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
	}, buf
}
