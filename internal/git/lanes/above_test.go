package lanes

import (
	"math/rand/v2"
	"testing"
)

// layoutOf lays out rows from to to as their own layout.
func layoutOf(rows [][]uint32, from, to int) *Layout {
	var l Layout
	for i := from; i < to; i++ {
		l.Append(uint32(i), rows[i])
	}
	return &l
}

// sameRows checks that s draws what a layout of all rows at once draws,
// colors aside: a lane's color is not the same number, but the same
// lanes share one.
func sameRows(t *testing.T, s *Snapshot, rows [][]uint32) {
	t.Helper()
	whole := build(rows).Snapshot()
	if s.Len() != whole.Len() || s.Width() != whole.Width() {
		t.Fatalf("%d rows %d wide, laid out whole %d rows %d wide", s.Len(), s.Width(), whole.Len(), whole.Width())
	}
	to, from := map[uint16]uint16{}, map[uint16]uint16{}
	same := func(i int, a, b uint16) {
		if x, ok := to[b]; ok && x != a || !ok && from[a] != 0 && from[a] != b+1 {
			t.Fatalf("row %d: color %d where laid out whole it is %d, mapped to %d", i, a, b, x)
		}
		to[b], from[a] = a, b+1
	}
	var buf, wbuf []Edge
	// In order, then back to front, so the cursor crosses segments both
	// ways and starts from checkpoints.
	order := make([]int, 0, 2*s.Len())
	for i := range s.Len() {
		order = append(order, i)
	}
	for i := s.Len() - 1; i >= 0; i -= 3 {
		order = append(order, i)
	}
	for _, i := range order {
		if s.Commit(i) != whole.Commit(i) {
			t.Fatalf("row %d: commit %d, laid out whole %d", i, s.Commit(i), whole.Commit(i))
		}
		var r, w Row
		r, buf = s.Row(i, 0, buf)
		w, wbuf = whole.Row(i, 0, wbuf)
		if r.Node != w.Node || r.Merges != w.Merges || len(r.Top) != len(w.Top) || len(r.Bottom) != len(w.Bottom) {
			t.Fatalf("row %d: %+v, laid out whole %+v", i, r, w)
		}
		same(i, r.Color, w.Color)
		for k, e := range append(r.Top, r.Bottom...) {
			we := append(w.Top, w.Bottom...)[k]
			if e.From != we.From || e.To != we.To {
				t.Fatalf("row %d: edge %v, laid out whole %v", i, e, we)
			}
			same(i, e.Color, we.Color)
		}
	}
}

// Rows laid out over others are drawn as the whole laid out at once,
// whenever their lanes all go down into the first row below; otherwise
// Above says so.
func TestAbove(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	took := 0
	for n := 0; n < 300; n++ {
		count := 2 + rng.IntN(200)
		rows := make([][]uint32, count)
		for i := range rows {
			if i == count-1 || rng.IntN(25) == 0 {
				continue
			}
			k := 1
			if rng.IntN(6) == 0 {
				k += 1 + rng.IntN(2)
			}
			seen := map[uint32]bool{}
			for j := 0; j < k; j++ {
				p := uint32(i + 1 + rng.IntN(min(6, count-i-1)))
				if !seen[p] {
					seen[p] = true
					rows[i] = append(rows[i], p)
				}
			}
		}
		// Cut into up to four layouts, laid over each other from the
		// bottom up as commits would come.
		cuts := []int{count}
		for c := count; len(cuts) < 4 && c > 1; {
			c = 1 + rng.IntN(c-1)
			cuts = append(cuts, c)
		}
		cuts = append(cuts, 0)
		s := layoutOf(rows, cuts[1], cuts[0]).Snapshot()
		for k := 1; k+1 < len(cuts); k++ {
			from, to := cuts[k+1], cuts[k]
			l := layoutOf(rows, from, to)
			fits := len(l.lanes) == 1 && l.lanes[0] == uint32(to)
			got, ok := l.Above(s)
			if ok != fits {
				t.Fatalf("history %d, rows %d to %d over %d: above %v, lanes %v", n, from, to, s.Len(), ok, l.lanes)
			}
			if !ok {
				s = layoutOf(rows, from, count).Snapshot()
				continue
			}
			took++
			s = got
		}
		sameRows(t, s, rows)
	}
	if took < 20 {
		t.Fatalf("only %d layouts fit over another", took)
	}
	t.Logf("%d laid over others", took)
}

// A commit at a time on top: each its own layout, its color the
// branch's, the rows below as they were.
func TestAboveOneByOne(t *testing.T) {
	rows := [][]uint32{}
	const n = maxSegments
	for i := range n {
		if i == n-1 {
			rows = append(rows, nil)
		} else {
			rows = append(rows, []uint32{uint32(i + 1)})
		}
	}
	s := layoutOf(rows, n-1, n).Snapshot()
	for i := n - 2; i >= 0; i-- {
		l := layoutOf(rows, i, i+1)
		var ok bool
		if s, ok = l.Above(s); !ok {
			t.Fatalf("commit %d does not fit on top", i)
		}
	}
	sameRows(t, s, rows)
	r, _ := s.Row(0, 0, nil)
	last, _ := s.Row(n-1, 0, nil)
	if r.Color != last.Color {
		t.Fatalf("the branch changes color: %d on top, %d below", r.Color, last.Color)
	}
	// One more would stack too many: the whole is laid out anew.
	var l Layout
	l.Append(n, []uint32{0})
	if _, ok := l.Above(s); ok || len(s.segs) != maxSegments {
		t.Fatalf("%d layouts stacked, and one more fits: %v", len(s.segs), ok)
	}
}
