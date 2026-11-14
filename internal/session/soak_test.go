package session

import (
	"runtime"
	"testing"
	"time"

	"ramus/internal/git/diff"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

// Opening and closing a repository many times, with every kind of read
// in flight at close, leaves no goroutine, no git process and no memory
// behind (Fork #2704, Sublime Merge #973).
func TestSoak(t *testing.T) {
	if testing.Short() {
		t.Skip("opens a repository 30 times")
	}
	r := gittest.Init(t)
	for i := range 20 {
		r.Write("a.txt", string(rune('a'+i))+"\n")
		r.Commit("work")
	}
	r.Write("a.txt", "changed\n")
	runner := testrun.Runner(t)

	cycle := func(early bool) {
		m := make(mainThread, 256)
		s := Open(runner, trusted(t, r.GitDir()), r.Dir, m.update)
		m.until(t, "status", func() bool { return s.State.Status != nil && s.State.Refs != nil })
		s.LoadHistory()
		s.SetView("file")
		s.FileLog("a.txt", FileLogOptions{Follow: true}, func([]FileCommit, bool, error) {})
		cancel := s.Blame("a.txt", func([]byte, error) {}, func(BlameUpdate) {})
		s.LoadCommit(s.State.Head.OID, func(CommitDetail) {})
		s.Patch(diff.PatchRequest{Source: diff.Unstaged, Paths: []string{"a.txt"}, Context: -1}, Sides{WorkTreePath: "a.txt"}, func(PatchResult) {})
		if !early {
			m.until(t, "history", func() bool { return s.History.Done })
		}
		cancel()
		s.Close()
		select {
		case <-s.Done():
		case <-time.After(20 * time.Second):
			t.Fatal("the session did not end")
		}
	}
	settle := func() (goroutines int, heap uint64) {
		deadline := time.Now().Add(5 * time.Second)
		for {
			runtime.GC()
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			n := runtime.NumGoroutine()
			if runner.Running() == 0 || time.Now().After(deadline) {
				return n, ms.HeapAlloc
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	cycle(false) // warm what lives as long as the process
	g0, heap0 := settle()
	for i := range 30 {
		cycle(i%2 == 0)
	}
	// Goroutines that are ending take a moment.
	var g1 int
	var heap1 uint64
	for range 50 {
		if g1, heap1 = settle(); g1 <= g0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Logf("after 30 opens: goroutines %d (were %d), heap %d KB (was %d KB), git processes %d",
		g1, g0, heap1>>10, heap0>>10, runner.Running())
	if g1 > g0 {
		buf := make([]byte, 1<<20)
		t.Fatalf("%d goroutines left behind:\n%s", g1-g0, buf[:runtime.Stack(buf, true)])
	}
	if runner.Running() != 0 {
		t.Fatalf("%d git processes left behind", runner.Running())
	}
	if heap1 > heap0+8<<20 {
		t.Fatalf("heap grew from %d KB to %d KB", heap0>>10, heap1>>10)
	}
}
