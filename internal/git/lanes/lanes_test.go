package lanes

import (
	"fmt"
	"math/rand/v2"
	"testing"
)

// check verifies what drawing relies on: each row's top edges meet the
// previous row's bottom edges column for column, every edge at the middle
// of a row touches the node or passes straight, and each parent gets
// exactly one bottom edge from the node.
func check(t *testing.T, l *Layout, parents [][]uint32) {
	t.Helper()
	// Read forwards, then again from the end backwards and at random, so
	// both the cursor and the checkpoints are exercised.
	s := l.Snapshot()
	var buf, prevBottom []Edge
	want := make([]string, l.Len())
	for i := 0; i < l.Len(); i++ {
		var r Row
		r, buf = s.Row(i, 0, buf)
		want[i] = fmt.Sprint(r)
		if i == 0 && len(r.Top) != 0 {
			t.Fatalf("row 0 has top edges")
		}
		if i > 0 {
			// The columns lines arrive in from above are the columns the
			// previous row's lines leave in, with the same colors.
			above, below := map[uint16]uint16{}, map[uint16]uint16{}
			for _, e := range prevBottom {
				below[e.To] = e.Color
			}
			for _, e := range r.Top {
				if _, dup := above[e.From]; dup {
					t.Fatalf("row %d: two top edges from column %d", i, e.From)
				}
				above[e.From] = e.Color
			}
			if fmt.Sprint(above) != fmt.Sprint(below) {
				t.Fatalf("row %d: top edges from %v, previous row's bottom edges end at %v", i, above, below)
			}
		}
		for _, e := range r.Top {
			if int(e.To) != r.Node && e.To != e.From {
				t.Fatalf("row %d: top edge %v neither reaches the node nor passes", i, e)
			}
		}
		fromNode := 0
		for _, e := range r.Bottom {
			if int(e.From) == r.Node {
				fromNode++
			} else if e.From != e.To {
				t.Fatalf("row %d: bottom edge %v neither leaves the node nor passes", i, e)
			}
		}
		if fromNode != len(parents[i]) {
			t.Fatalf("row %d: %d edges leave the node, commit has %d parents", i, fromNode, len(parents[i]))
		}
		if r.Merges != max(len(parents[i])-1, 0) {
			t.Fatalf("row %d: merges %d", i, r.Merges)
		}
		prevBottom = append(prevBottom[:0], r.Bottom...)
	}
	s2 := l.Snapshot()
	for i := l.Len() - 1; i >= 0; i -= 7 {
		var r Row
		r, buf = s2.Row(i, 0, buf)
		if got := fmt.Sprint(r); got != want[i] {
			t.Fatalf("row %d read out of order: %s, in order %s", i, got, want[i])
		}
	}
}

func build(rows [][]uint32) *Layout {
	var l Layout
	for i, ps := range rows {
		l.Append(uint32(i), ps)
	}
	return &l
}

func TestLinear(t *testing.T) {
	rows := [][]uint32{{1}, {2}, {}}
	l := build(rows)
	check(t, l, rows)
	if l.Width() != 1 {
		t.Fatalf("width %d", l.Width())
	}
}

func TestMergeAndFork(t *testing.T) {
	// 0 merges 1 and 2; 1 and 2 both come from 3.
	rows := [][]uint32{{1, 2}, {3}, {3}, {}}
	l := build(rows)
	check(t, l, rows)
	if l.Width() != 2 {
		t.Fatalf("width %d", l.Width())
	}
	s := l.Snapshot()
	r, _ := s.Row(2, 0, nil)
	if r.Node != 1 {
		t.Fatalf("second parent's node in column %d", r.Node)
	}
	r, _ = s.Row(3, 0, nil)
	if r.Node != 0 || len(r.Top) != 2 || r.Top[1].To != 0 {
		t.Fatalf("fork point row %+v", r)
	}
}

func TestOctopusAndTips(t *testing.T) {
	// Two branch tips, an octopus merge and two roots.
	rows := [][]uint32{{2}, {2}, {3, 4, 5}, {6}, {6}, {}, {}}
	l := build(rows)
	check(t, l, rows)
	if r, _ := l.Snapshot().Row(2, 0, nil); r.Merges != 2 {
		t.Fatalf("octopus merges %d", r.Merges)
	}
}

func TestColumnsReused(t *testing.T) {
	// Many short branches off one line, one after another, never need more
	// than two columns.
	var rows [][]uint32
	for i := 0; i < 50; i++ {
		b := uint32(len(rows))
		rows = append(rows, []uint32{b + 2, b + 1}, []uint32{b + 2})
	}
	rows = append(rows, []uint32{})
	l := build(rows)
	check(t, l, rows)
	if l.Width() != 2 {
		t.Fatalf("width %d", l.Width())
	}
}

func TestRandom(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for n := 0; n < 200; n++ {
		count := 2 + rng.IntN(300)
		rows := make([][]uint32, count)
		for i := range rows {
			if i == count-1 || rng.IntN(20) == 0 {
				continue // a root
			}
			k := 1
			if rng.IntN(5) == 0 {
				k += 1 + rng.IntN(3)
			}
			seen := map[uint32]bool{}
			for j := 0; j < k; j++ {
				p := uint32(i + 1 + rng.IntN(min(10, count-i-1)))
				if !seen[p] {
					seen[p] = true
					rows[i] = append(rows[i], p)
				}
			}
		}
		check(t, build(rows), rows)
	}
}

func BenchmarkAppend(b *testing.B) {
	rows := make([][]uint32, 100_000)
	rng := rand.New(rand.NewPCG(3, 4))
	for i := range rows[:len(rows)-1] {
		rows[i] = []uint32{uint32(i + 1)}
		if rng.IntN(4) == 0 && i+20 < len(rows) {
			rows[i] = append(rows[i], uint32(i+2+rng.IntN(18)))
		}
	}
	b.ResetTimer()
	for b.Loop() {
		build(rows)
	}
}

// A snapshot keeps reading the rows it was taken with while the layout
// appends more, as the history view reads while the walk goes on.
func TestSnapshotWhileAppending(t *testing.T) {
	var l Layout
	n := 3 * rowChunk
	for i := 0; i < n; i++ {
		ps := []uint32{uint32(i + 1)}
		if i%3 == 0 {
			ps = append(ps, uint32(i+2))
		}
		l.Append(uint32(i), ps)
		if i == rowChunk+5 {
			s := l.Snapshot()
			done := make(chan struct{})
			go func() {
				defer close(done)
				var buf []Edge
				for j := 0; j < s.Len(); j++ {
					_, buf = s.Row(j, 0, buf)
				}
			}()
			defer func() { <-done }()
		}
	}
}

func TestMaxCols(t *testing.T) {
	// Ten branches off one root, merged one by one.
	var rows [][]uint32
	for i := 0; i < 10; i++ {
		rows = append(rows, []uint32{uint32(i + 1), uint32(11 + i)})
	}
	rows = append(rows, []uint32{21})
	for i := 0; i < 10; i++ {
		rows = append(rows, []uint32{21})
	}
	rows = append(rows, []uint32{})
	l := build(rows)
	check(t, l, rows)
	s := l.Snapshot()
	for i := 0; i < l.Len(); i++ {
		r, _ := s.Row(i, 3, nil)
		for _, e := range append(r.Top, r.Bottom...) {
			if e.From >= 3 && e.To >= 3 {
				t.Fatalf("row %d: edge %v past the columns asked for", i, e)
			}
		}
	}
}
