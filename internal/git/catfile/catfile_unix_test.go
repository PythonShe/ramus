//go:build unix

package catfile

import (
	"context"
	"errors"
	"strings"
	"syscall"
	"testing"
	"time"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/run"
)

// A git that stops answering, as on a stalled network file system, is
// ended after a while rather than holding every read behind it.
func TestStalledGitIsEnded(t *testing.T) {
	r := gittest.Init(t)
	c := r.Commit("first")
	p, err := Start(context.Background(), testrun.Runner(t), r.GitDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.mu.Lock()
	p.stall = 100 * time.Millisecond
	p.mu.Unlock()
	if err := p.g.cmd.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	done := make(chan error)
	go func() { done <- p.Read([][]byte{decode(c)}, func(int, Object) error { return nil }) }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrClosed) || !strings.Contains(err.Error(), run.ErrStalled.Error()) {
			t.Fatalf("stalled read: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a stalled git holds the read")
	}
	if p.Alive() {
		t.Fatal("alive once stalled")
	}
}
