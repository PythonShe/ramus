package session

import (
	"testing"
	"time"

	"ramus/internal/git/gittest"
)

func TestStatusPause(t *testing.T) {
	var p statusPause
	t0 := time.Now()
	if w := p.wait(t0, false); w != 0 {
		t.Fatalf("no status yet: waits %v", w)
	}
	p.ran(t0, time.Second)
	if w := p.wait(t0.Add(500*time.Millisecond), false); w != 1500*time.Millisecond {
		t.Fatalf("within the pause: waits %v", w)
	}
	if w := p.wait(t0.Add(500*time.Millisecond), true); w != 0 {
		t.Fatalf("urgent: waits %v", w)
	}
	if w := p.wait(t0.Add(2*time.Second), false); w != 0 {
		t.Fatalf("after the pause: waits %v", w)
	}
}

// Full statuses are spaced out by twice what the last took, unless the
// user asks.
func TestFullStatusWaitsOutThePause(t *testing.T) {
	const took = 400 * time.Millisecond
	runner, logged := scriptRunner(t, `case "$*" in *"status --porcelain=v2"*) sleep 0.4;; esac`)
	r := gittest.Init(t)
	r.Write("a", "1")
	r.Commit("one")
	m := make(mainThread, 64)
	s := Open(runner, trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "status", func() bool { return s.State.Status != nil })
	m.settle(t, s)

	r.Write("new1", "x")
	m.until(t, "the first new file", func() bool { return s.State.Status.Len() == 1 })
	shown := time.Now()
	r.Write("new2", "x")
	m.until(t, "the second new file", func() bool { return s.State.Status.Len() == 2 })
	last := time.Now()
	// It waited out 2*took from the end of the last, then took as long.
	if d := time.Since(shown); d < 2*took {
		t.Fatalf("the next full status showed %v after the last", d)
	}

	m.pump(Debounce)
	before, read := fullStatusRuns(logged()), s.State.Refreshed
	s.Reload()
	m.until(t, "the reload", func() bool { return !s.State.Refreshed.Equal(read) })
	// One that waited out the pause would show 2*took after the last
	// ended, and take took more.
	if d := time.Since(last); d >= 3*took || fullStatusRuns(logged()) == before {
		t.Fatalf("a reload the user asked for showed %v after the last status", d)
	}
}
