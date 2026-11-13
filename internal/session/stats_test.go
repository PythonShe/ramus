package session

import (
	"strings"
	"testing"
	"time"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

func TestStats(t *testing.T) {
	r := gittest.Init(t)
	r.Write(".mailmap", "Main Author <test@example.com>\n")
	var times []int64
	for i := range 6 {
		r.Write("a", strings.Repeat("x", i))
		r.Commit("main")
		times = append(times, int64(1_700_000_000+60*(i+1)))
	}
	r.Git("checkout", "-q", "-b", "side", "HEAD~3")
	r.Write("b", "1")
	r.Commit("side")

	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "refs", func() bool { return s.State.Refs != nil })

	count := func(req StatsRequest) StatsResult {
		t.Helper()
		var res *StatsResult
		s.LoadStats(req, func(sr StatsResult) {
			if sr.Done {
				res = &sr
			}
		})
		m.until(t, "the counts", func() bool { return res != nil })
		if res.Err != nil {
			t.Fatal(res.Err)
		}
		return *res
	}
	for _, graph := range []bool{false, true} {
		if graph {
			r.Git("commit-graph", "write", "--reachable")
		}
		all := count(StatsRequest{})
		if all.Stats.Commits != 7 || len(all.Stats.Authors) != 1 || all.Stats.Authors[0].Name != "Main Author" {
			t.Fatalf("graph %v: all %+v", graph, all.Stats)
		}
		if main := count(StatsRequest{Branch: "refs/heads/main"}); main.Stats.Commits != 6 {
			t.Fatalf("graph %v: main %d", graph, main.Stats.Commits)
		}
		// Commit times are the gittest clock: 1,700,000,060 onwards.
		in := count(StatsRequest{Since: time.Unix(times[1], 0), Until: time.Unix(times[4], 0)})
		if in.Stats.Commits != 3 {
			t.Fatalf("graph %v: in range %d", graph, in.Stats.Commits)
		}
	}
}
