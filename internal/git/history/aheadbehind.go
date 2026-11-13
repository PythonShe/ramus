package history

import (
	"context"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"ramus/internal/git/commitgraph"
	"ramus/internal/git/run"
)

// AheadBehind counts the commits reachable from a but not b (ahead) and
// from b but not a (behind), as git rev-list --left-right --count a...b
// does, from the commit-graph: both sides are walked down by generation,
// and the walk stops once every commit still queued is reachable from
// both.
func AheadBehind(g *commitgraph.Graph, a, b uint32) (ahead, behind int, err error) {
	const left, right = 1, 2
	flags := map[uint32]uint8{}
	var q queue
	stale := 0 // queued commits that both sides reach
	push := func(pos uint32, f uint8) {
		old := flags[pos]
		if old|f == old {
			return
		}
		flags[pos] = old | f
		if old == 0 {
			q.push(entry{gen: g.Generation(pos), time: g.CommitTime(pos), pos: pos})
			if old|f == left|right {
				stale++
			}
			return
		}
		// Already queued with one side; now it has both.
		stale++
	}
	push(a, left)
	push(b, right)
	var parents []uint32
	for q.len() > stale {
		e := q.pop()
		f := flags[e.pos]
		if f == left|right {
			stale--
		}
		switch f {
		case left:
			ahead++
		case right:
			behind++
		}
		if parents, err = g.Parents(e.pos, parents[:0]); err != nil {
			return 0, 0, err
		}
		for _, p := range parents {
			push(p, f)
		}
	}
	return ahead, behind, nil
}

// AheadBehindGit asks git, for tips the commit-graph does not cover.
func AheadBehindGit(ctx context.Context, r *run.Runner, gitDir string, a, b []byte) (ahead, behind int, err error) {
	res, err := r.Run(ctx, &run.Cmd{GitDir: gitDir, Read: true,
		Args: []string{"rev-list", "--left-right", "--count", hex.EncodeToString(a) + "..." + hex.EncodeToString(b)}})
	if err != nil {
		return 0, 0, err
	}
	f := strings.Fields(string(res.Stdout))
	if len(f) != 2 {
		return 0, 0, fmt.Errorf("history: rev-list --count printed %q", res.Stdout)
	}
	if ahead, err = strconv.Atoi(f[0]); err != nil {
		return 0, 0, err
	}
	behind, err = strconv.Atoi(f[1])
	return ahead, behind, err
}
