package history

import (
	"cmp"
	"slices"

	"ramus/internal/git/commitgraph"
)

// TopoWalk visits commits in git's --topo-order: each commit before its
// parents, and a merged branch's commits together, right after the merge
// that brought them in, rather than interleaved by date. That keeps the
// graph narrow.
//
// It is git's incremental topological walk: children are counted only down
// to the lowest generation the walk has reached, so the first rows come out
// without reading the whole history. Unlike git, it does not count down to
// the oldest tip before the first row: a tip is checked for children when
// the walk comes to it, so an old tag does not hold up the newest commits.
type TopoWalk struct {
	g *commitgraph.Graph
	// indegree is 0 for a commit not yet reached, else 1 plus the number of
	// its children not yet emitted.
	indegree []uint32
	// pending holds commits whose children are still being counted, highest
	// generation first.
	pending queue
	minGen  uint64
	// ready is a stack: the last parent pushed is emitted first, so a merged
	// branch follows its merge. It starts with every tip, newest on top,
	// each to be checked when it comes up: one another tip reaches waits
	// for its children instead.
	ready   []uint32
	tip     []bool
	parents []uint32
	scratch []uint32
	err     error
}

// NewTopoWalk starts a walk at tips, positions in g, newest first.
func NewTopoWalk(g *commitgraph.Graph, tips []uint32) *TopoWalk {
	w := &TopoWalk{g: g, indegree: make([]uint32, g.Len()), minGen: ^uint64(0), tip: make([]bool, g.Len())}
	var starts []uint32
	for _, t := range tips {
		if w.indegree[t] != 0 {
			continue
		}
		w.indegree[t] = 1
		w.pending.push(entry{gen: g.Generation(t), time: g.CommitTime(t), pos: t})
		starts = append(starts, t)
	}
	// Tips start the walk newest commit date on top, ties in the order
	// given, as git sorts them.
	slices.SortStableFunc(starts, func(a, b uint32) int {
		return cmp.Compare(g.CommitTime(b), g.CommitTime(a))
	})
	for i := len(starts) - 1; i >= 0; i-- {
		w.ready = append(w.ready, starts[i])
		w.tip[starts[i]] = true
	}
	return w
}

// emitted marks a commit the walk has returned.
const emitted = ^uint32(0)

// reach counts children down to gen, the generation of a commit about to
// be decided on.
func (w *TopoWalk) reach(gen uint64) {
	if gen < w.minGen {
		w.minGen = gen
		w.count(gen)
	}
}

// count finishes counting children for every commit of generation gen or
// above.
func (w *TopoWalk) count(gen uint64) {
	for w.pending.len() > 0 && w.pending.h[0].gen >= gen && w.err == nil {
		e := w.pending.pop()
		w.scratch, w.err = w.g.Parents(e.pos, w.scratch[:0])
		for _, p := range w.scratch {
			if w.indegree[p] != 0 {
				w.indegree[p]++
				continue
			}
			w.indegree[p] = 2
			w.pending.push(entry{gen: w.g.Generation(p), time: w.g.CommitTime(p), pos: p})
		}
	}
}

// Next returns the next commit and its parents; the parents slice is
// reused by the next call. ok is false at the end, or when a damaged graph
// stopped the walk; Err tells which.
func (w *TopoWalk) Next() (pos uint32, parents []uint32, ok bool) {
	for {
		if len(w.ready) == 0 || w.err != nil {
			return 0, nil, false
		}
		pos = w.ready[len(w.ready)-1]
		w.ready = w.ready[:len(w.ready)-1]
		if w.indegree[pos] == emitted {
			continue // a tip its last child pushed again, gone already
		}
		if !w.tip[pos] {
			break
		}
		// A tip as it came up from the start: it goes now if no other
		// tip reaches it; if one does, its last child pushes it again.
		// The children of every commit at or above its generation are
		// counted by then, so its count is whole.
		w.tip[pos] = false
		w.reach(w.g.Generation(pos))
		if w.err == nil && w.indegree[pos] == 1 {
			break
		}
	}
	w.indegree[pos] = emitted
	w.parents, w.err = w.g.Parents(pos, w.parents[:0])
	if w.err != nil {
		return 0, nil, false
	}
	for _, p := range w.parents {
		w.reach(w.g.Generation(p))
	}
	for _, p := range w.parents {
		if w.indegree[p] == 0 || w.indegree[p] == emitted {
			continue // a parent the counting walk has not reached: damaged
		}
		w.indegree[p]--
		if w.indegree[p] == 1 {
			w.ready = append(w.ready, p)
		}
	}
	if w.err != nil {
		return 0, nil, false
	}
	return pos, w.parents, true
}

// Err returns the error that ended the walk early, if any.
func (w *TopoWalk) Err() error { return w.err }
