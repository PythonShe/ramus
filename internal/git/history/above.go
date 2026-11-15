package history

import "slices"

// Above orders the commits that tips reach and a history already ordered
// does not, when git's --topo-order of the new history is them followed
// by the old one as it was: a commit on top of the newest branch, or a
// fetch moving it forward. Then only they need laying out, above the
// rows there are.
//
// in reports the commits of the old history; starts are its walk's
// starts, as TopoWalk.Starts or RevStarts gave them. tips are the new
// history's, in the order git is given them. It returns the new commits
// in order, and the starts of the new history, for the next time; ok is
// false when the old history would not go on as it was (a branch other
// than the newest moved, history was rewritten, or commits went), or when
// more than limit commits are new.
//
// It is the walk TopoWalk does, run over the new commits alone: they have
// children only among themselves, and the old commits' order stays when
// the walk, once it has given every new commit, stands where the old walk
// started.
func Above(src *Source, in func(id uint32) bool, starts, tips []uint32, limit int) (rows, newStarts []uint32, ok bool, err error) {
	// kids counts, for every commit a new one names as a parent, its
	// children among the new commits not yet given.
	kids := map[uint32]int{}
	fresh := map[uint32]bool{}
	var queue, ps []uint32
	for _, t := range tips {
		if !in(t) && !fresh[t] {
			fresh[t] = true
			queue = append(queue, t)
		}
	}
	for k := 0; k < len(queue); k++ {
		if k >= limit {
			return nil, nil, false, nil
		}
		if ps, err = src.Parents(queue[k], ps[:0]); err != nil {
			return nil, nil, false, err
		}
		for _, p := range ps {
			kids[p]++
			if !in(p) && !fresh[p] {
				fresh[p] = true
				queue = append(queue, p)
			}
		}
	}
	wasStart := map[uint32]bool{}
	for _, s := range starts {
		wasStart[s] = true
	}
	// A start, old or new, is a commit nothing in the new history has as
	// a parent.
	sorted := sortTips(src, tips)
	starter := func(t uint32) bool { return kids[t] == 0 && (fresh[t] || wasStart[t]) }
	for _, t := range sorted {
		if starter(t) {
			newStarts = append(newStarts, t)
		}
	}
	type entry struct {
		id  uint32
		tip bool
	}
	stack := make([]entry, 0, len(sorted))
	for i := len(sorted) - 1; i >= 0; i-- {
		stack = append(stack, entry{sorted[i], true})
	}
	given := map[uint32]bool{}
	for len(rows) < len(queue) {
		if len(stack) == 0 {
			return nil, nil, false, nil // a new commit no walk reaches: damaged
		}
		e := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		x := e.id
		switch {
		case fresh[x] && (given[x] || e.tip && kids[x] != 0):
			continue // gone already, or its last child pushes it
		case !fresh[x] && e.tip && !starter(x):
			continue // reached from elsewhere: no start
		case !fresh[x]:
			return nil, nil, false, nil // an old commit before a new one
		}
		given[x] = true
		rows = append(rows, x)
		if ps, err = src.Parents(x, ps[:0]); err != nil {
			return nil, nil, false, err
		}
		for _, p := range ps {
			kids[p]--
			if kids[p] == 0 && (fresh[p] || wasStart[p]) {
				stack = append(stack, entry{p, false})
			}
		}
	}
	// The old walk started with its starts on the stack, the newest on
	// top. What is left here must come out the same: the old commits that
	// start, each where it comes first from the top.
	var rest []uint32
	seen := map[uint32]bool{}
	for i := len(stack) - 1; i >= 0; i-- {
		x := stack[i].id
		if fresh[x] || stack[i].tip && !wasStart[x] || seen[x] {
			continue
		}
		seen[x] = true
		rest = append(rest, x)
	}
	if !slices.Equal(rest, starts) {
		return nil, nil, false, nil
	}
	return rows, newStarts, true, nil
}

// RevStarts returns the starts of a history git ordered (RevList), which
// src holds whole: the tips no commit of it has as a parent, newest first
// as TopoWalk.Starts gives them.
func RevStarts(src *Source, tips []uint32) ([]uint32, error) {
	isTip := map[uint32]bool{}
	for _, t := range tips {
		isTip[t] = true
	}
	var ps []uint32
	var err error
	for id := range uint32(src.Len()) {
		if !src.known(id) {
			continue
		}
		if ps, err = src.Parents(id, ps[:0]); err != nil {
			return nil, err
		}
		for _, p := range ps {
			delete(isTip, p)
		}
	}
	var out []uint32
	for _, t := range sortTips(src, tips) {
		if isTip[t] {
			out = append(out, t)
		}
	}
	return out, nil
}
