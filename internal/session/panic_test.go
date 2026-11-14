package session

import (
	"context"
	"strings"
	"sync"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/lifetime"
)

func TestPanicRecovered(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a.txt", "a\n")
	r.Commit("one")
	var mu sync.Mutex
	var heard []string
	OnPanic = func(path string, p lifetime.Panic) {
		mu.Lock()
		heard = append(heard, path+": "+p.Value)
		mu.Unlock()
	}
	t.Cleanup(func() { OnPanic = nil })
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "the status", func() bool { return s.State.Status != nil })

	// In the repository's work: a secret in the value is not kept.
	s.Repo.Life().Go(func(context.Context) { panic("token https://u:hunter2@example.com/x") })
	m.until(t, "the panic", func() bool { return s.Panic != nil })
	if strings.Contains(s.Panic.Value, "hunter2") || !strings.Contains(s.Panic.Stack, "TestPanicRecovered") {
		t.Fatalf("%+v", s.Panic)
	}
	// On the main thread, in what the work hands it: the first is kept.
	first := s.Panic
	s.update(func() { panic("in a callback") })
	m.until(t, "the second", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(heard) == 2
	})
	if s.Panic != first || !strings.HasSuffix(heard[1], ": in a callback") || !strings.HasPrefix(heard[0], s.Path+": ") {
		t.Fatalf("panic %+v, heard %q", s.Panic, heard)
	}
	// The session still closes.
}
