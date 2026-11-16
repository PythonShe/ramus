package write

import (
	"sync"
	"testing"
	"time"

	"ramus/internal/git/run"
)

// A write with Hold waits for what holds it, as a read of git-lfs's
// locks does, before it runs.
func TestHold(t *testing.T) {
	h := newHarness(t)
	var mu sync.Mutex
	mu.Lock()
	j := h.q.Submit(Op{Name: "Held", Hold: &mu, Cmd: run.Cmd{GitDir: h.w.GitDir, Args: []string{"config", "ramus.held", "yes"}}})
	select {
	case <-j.Done():
		t.Fatal("ran while held")
	case <-time.After(200 * time.Millisecond):
	}
	mu.Unlock()
	if _, err := wait(t, j); err != nil {
		t.Fatal(err)
	}
	if got := h.r.Git("config", "ramus.held"); got != "yes\n" {
		t.Fatalf("config %q", got)
	}
	// Ended, it lets go.
	if !mu.TryLock() {
		t.Fatal("still held")
	}
}
