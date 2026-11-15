package history

import (
	"cmp"
	"slices"
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
//
// Commits the source read from their objects have no generation: as git
// does, it gives them one above every commit of the graph, and counts the
// children of all of them before the first row. They are the few made
// since the graph was written.
type TopoWalk struct {
	src *Source
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
	ready []uint32
	tip   []bool
	// starts are the tips no other commit of the walk reaches, as they
	// started it: newest first.
	starts  []uint32
	parents []uint32
	scratch []uint32
	err     error
}

// NewTopoWalk starts a walk at tips, numbers in src, in the order given.
func NewTopoWalk(src *Source, tips []uint32) *TopoWalk {
	n := src.Len()
	w := &TopoWalk{src: src, indegree: make([]uint32, n), minGen: genInfinity, tip: make([]bool, n)}
	starts := sortTips(src, tips)
	for _, t := range starts {
		w.indegree[t] = 1
		w.pending.push(entry{gen: src.Generation(t), time: src.CommitTime(t), pos: t})
	}
	for i := len(starts) - 1; i >= 0; i-- {
		w.ready = append(w.ready, starts[i])
		w.tip[starts[i]] = true
	}
	// The commits read from their objects all have the one generation
	// above the graph's: their children are counted at once.
	w.count(genInfinity)
	return w
}

// sortTips returns tips without repeats, newest commit date first, ties
// in the order given, as git sorts them to start a walk.
func sortTips(src *Source, tips []uint32) []uint32 {
	var out []uint32
	seen := map[uint32]bool{}
	for _, t := range tips {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	slices.SortStableFunc(out, func(a, b uint32) int {
		return cmp.Compare(src.CommitTime(b), src.CommitTime(a))
	})
	return out
}

// Starts returns the tips no other commit of the walk reaches, newest
// first, as the walk has found them so far: all of them once it ended.
// They are where history ordered anew from more tips can be checked to
// go on as this one did (Above).
func (w *TopoWalk) Starts() []uint32 { return w.starts }

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
		w.scratch, w.err = w.src.Parents(e.pos, w.scratch[:0])
		for _, p := range w.scratch {
			if w.indegree[p] != 0 {
				w.indegree[p]++
				continue
			}
			w.indegree[p] = 2
			w.pending.push(entry{gen: w.src.Generation(p), time: w.src.CommitTime(p), pos: p})
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
		w.reach(w.src.Generation(pos))
		if w.err == nil && w.indegree[pos] == 1 {
			w.starts = append(w.starts, pos)
			break
		}
	}
	w.indegree[pos] = emitted
	w.parents, w.err = w.src.Parents(pos, w.parents[:0])
	if w.err != nil {
		return 0, nil, false
	}
	for _, p := range w.parents {
		w.reach(w.src.Generation(p))
	}
	for _, p := range w.parents {
		if w.indegree[p] == 0 || w.indegree[p] == emitted {
			continue // a parent the counting walk has not reached: damaged
		}
		w.indegree[p]--
		if w.indegree[p] == 1 {
			// A tip pushed by its last child is no start: its entry from
			// the start finds it gone.
			w.ready, w.tip[p] = append(w.ready, p), false
		}
	}
	if w.err != nil {
		return 0, nil, false
	}
	return pos, w.parents, true
}

// Err returns the error that ended the walk early, if any.
func (w *TopoWalk) Err() error { return w.err }
