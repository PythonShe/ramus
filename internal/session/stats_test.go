package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/gitexe"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/run"
)

// The cat-file processes a count starts beside the repository's own are
// bounded across the app: with none free it reads with the one it has,
// and gives back those it took.
func TestStatsReadersBounded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as git")
	}
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip(err)
	}
	r := gittest.Init(t)
	for i := range 3 {
		r.Write("a", strings.Repeat("x", i))
		r.Commit("c")
	}
	bin := t.TempDir()
	log := filepath.Join(bin, "log")
	// A reader given no commit is ended as soon as it starts: the script
	// ignores that until it has logged, then git ends as its input closes.
	script := "#!/bin/sh\ntrap '' TERM\nfor a in \"$@\"; do [ \"$a\" = cat-file ] && echo x >> " + log + "; done\nexec " + real + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	g, err := gitexe.Find(t.Context(), bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err != nil {
		t.Fatal(err)
	}
	s, m := openWith(t, r, run.New(g, gittest.Env(), 8))
	m.until(t, "refs", func() bool { return s.State.Refs != nil })
	starts := func() int { b, _ := os.ReadFile(log); return strings.Count(string(b), "x") }
	count := func() int {
		t.Helper()
		before := starts()
		var res *StatsResult
		s.LoadStats(StatsRequest{}, func(sr StatsResult) {
			if sr.Done {
				res = &sr
			}
		})
		m.until(t, "the counts", func() bool { return res != nil })
		if res.Err != nil || res.Stats.Commits != 3 {
			t.Fatalf("%+v", *res)
		}
		return starts() - before
	}
	count() // the repository's own cat-file starts
	for range cap(readerSlots) {
		readerSlots <- struct{}{}
	}
	if n := count(); n != 0 {
		t.Errorf("with no slot free, %d more cat-file started", n)
	}
	for range cap(readerSlots) {
		<-readerSlots
	}
	if n := count(); n != statsReaders-1 {
		t.Errorf("with every slot free, %d more cat-file started, want %d", n, statsReaders-1)
	}
	if n := len(readerSlots); n != 0 {
		t.Errorf("%d slots kept after the count", n)
	}
}

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
