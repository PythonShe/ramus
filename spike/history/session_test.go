package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/trust"
	"ramus/internal/session"
)

// TestSessionKernel times the session's history load on the kernel, from
// the refs read to the first screen and to the whole history:
//
//	RAMUS_FIXTURES=~/Projects/ramus-fixtures go test ./spike/history -run SessionKernel -v
func TestSessionKernel(t *testing.T) {
	dir := filepath.Join(gittest.FixturesDir(t), "linux")
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Skip(err)
	}
	ts, err := trust.Load(filepath.Join(t.TempDir(), "trust.json"))
	if err != nil {
		t.Fatal(err)
	}
	updates := make(chan func(), 1024)
	s := session.Open(testrun.Runner(t), ts, dir, func(fn func()) { updates <- fn })
	t.Cleanup(func() { s.Close(); <-s.Done() })
	deadline := time.After(5 * time.Minute)
	pump := func(ok func() bool) {
		for !ok() {
			select {
			case fn := <-updates:
				fn()
			case <-deadline:
				t.Fatal("timed out")
			}
		}
	}
	pump(func() bool { return s.State.Refs != nil })
	start := time.Now()
	s.LoadHistory()
	var first time.Duration
	pump(func() bool {
		if first == 0 && s.History.Snap != nil {
			first = time.Since(start)
		}
		return s.History.Done
	})
	h := s.History
	if h.Err != nil {
		t.Fatal(h.Err)
	}
	t.Logf("native=%v rows=%d first screen published %v (seen %v), whole %v (seen %v)",
		h.Native, h.Snap.Len(), h.First, first, h.Took, time.Since(start))
}
