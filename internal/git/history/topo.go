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
// without reading the whole history.
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
	// branch follows its merge.
	ready   []uint32
	parents []uint32
	scratch []uint32
	err     error
}

// NewTopoWalk starts a walk at tips, positions in g, newest first.
func NewTopoWalk(g *commitgraph.Graph, tips []uint32) *TopoWalk {
	w := &TopoWalk{g: g, indegree: make([]uint32, g.Len()), minGen: ^uint64(0)}
	var starts []uint32
	for _, t := range tips {
		if w.indegree[t] != 0 {
			continue
		}
		w.indegree[t] = 1
		gen := g.Generation(t)
		w.pending.push(entry{gen: gen, time: g.CommitTime(t), pos: t})
		w.minGen = min(w.minGen, gen)
		starts = append(starts, t)
	}
	w.count(w.minGen)
	// Tips that no other tip reaches start the walk, newest commit date on
	// top and ties in the order given, as git sorts them.
	slices.SortStableFunc(starts, func(a, b uint32) int {
		return cmp.Compare(g.CommitTime(b), g.CommitTime(a))
	})
	for i := len(starts) - 1; i >= 0; i-- {
		if w.indegree[starts[i]] == 1 {
			w.ready = append(w.ready, starts[i])
		}
	}
	return w
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
	if len(w.ready) == 0 || w.err != nil {
		return 0, nil, false
	}
	pos = w.ready[len(w.ready)-1]
	w.ready = w.ready[:len(w.ready)-1]
	w.parents, w.err = w.g.Parents(pos, w.parents[:0])
	if w.err != nil {
		return 0, nil, false
	}
	for _, p := range w.parents {
		if gen := w.g.Generation(p); gen < w.minGen {
			w.minGen = gen
			w.count(gen)
		}
	}
	for _, p := range w.parents {
		if w.indegree[p] == 0 {
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
