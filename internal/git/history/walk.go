// Package history orders a repository's commits for display.
package history

import (
	"ramus/internal/git/commitgraph"
)

// Walk visits the commits reachable from a set of tips in display order:
// each commit before its parents, highest generation first, which with
// corrected commit dates is newest first. Because a child's generation is
// always above its parents', the order is topological without counting
// children first, so the first rows come out at once, however long the
// history.
type Walk struct {
	g       *commitgraph.Graph
	queue   queue
	seen    []uint64 // one bit per graph position
	parents []uint32
	err     error
}

// NewWalk starts a walk at tips, which are positions in g.
func NewWalk(g *commitgraph.Graph, tips []uint32) *Walk {
	w := &Walk{g: g, seen: make([]uint64, (g.Len()+63)/64)}
	for _, t := range tips {
		w.push(t)
	}
	return w
}

func (w *Walk) push(pos uint32) {
	word, bit := pos/64, uint64(1)<<(pos%64)
	if w.seen[word]&bit != 0 {
		return
	}
	w.seen[word] |= bit
	w.queue.push(entry{gen: w.g.Generation(pos), time: w.g.CommitTime(pos), pos: pos})
}

// Next returns the next commit and its parents. The parents slice is
// reused by the next call. ok is false at the end of the walk, or after a
// damaged graph stopped it; Err tells which.
func (w *Walk) Next() (pos uint32, parents []uint32, ok bool) {
	if w.queue.len() == 0 || w.err != nil {
		return 0, nil, false
	}
	e := w.queue.pop()
	w.parents, w.err = w.g.Parents(e.pos, w.parents[:0])
	if w.err != nil {
		return 0, nil, false
	}
	for _, p := range w.parents {
		w.push(p)
	}
	return e.pos, w.parents, true
}

// Err returns the error that ended the walk early, if any.
func (w *Walk) Err() error { return w.err }

type entry struct {
	gen  uint64
	time int64
	pos  uint32
}

func (a entry) before(b entry) bool {
	if a.gen != b.gen {
		return a.gen > b.gen
	}
	if a.time != b.time {
		return a.time > b.time
	}
	return a.pos < b.pos
}

// queue is a binary heap of pointer-free entries.
type queue struct{ h []entry }

func (q *queue) len() int { return len(q.h) }

func (q *queue) push(e entry) {
	q.h = append(q.h, e)
	i := len(q.h) - 1
	for i > 0 {
		p := (i - 1) / 2
		if !q.h[i].before(q.h[p]) {
			break
		}
		q.h[i], q.h[p] = q.h[p], q.h[i]
		i = p
	}
}

func (q *queue) pop() entry {
	top := q.h[0]
	last := len(q.h) - 1
	q.h[0] = q.h[last]
	q.h = q.h[:last]
	i := 0
	for {
		l, r, m := 2*i+1, 2*i+2, i
		if l < last && q.h[l].before(q.h[m]) {
			m = l
		}
		if r < last && q.h[r].before(q.h[m]) {
			m = r
		}
		if m == i {
			return top
		}
		q.h[i], q.h[m] = q.h[m], q.h[i]
		i = m
	}
}
