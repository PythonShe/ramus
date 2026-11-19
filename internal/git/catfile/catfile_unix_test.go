//go:build unix

package catfile

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/run"
)

// A git that exited cleanly closes cleanly, although something it left
// running still holds its error output past WaitDelay: exec says
// ErrWaitDelay then, which is not a failure of git's.
func TestCloseAfterCleanExitWithOutputHeld(t *testing.T) {
	// Started without a context, so that ending it does not kill it: it
	// has exited 0 by the time Wait gives up on its output. What it left
	// running holds its error output until the test lets it go.
	hold, release, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Close()
	defer release.Close()
	cmd := exec.Command("/bin/sh", "-c", "cat <&3 >/dev/null & exit 0")
	cmd.ExtraFiles = []*os.File{hold}
	cmd.WaitDelay = 50 * time.Millisecond
	g := &proc{cmd: cmd, cancel: func() {}, untrack: func() {}, stderr: &limitedWriter{n: 1 << 10}}
	cmd.Stderr = g.stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	g.stdin = stdin
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &Process{batch: make(chan struct{}, 1), g: g}
	if err := p.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !errors.Is(g.err, exec.ErrWaitDelay) {
		t.Fatalf("Wait said %v, not ErrWaitDelay: the case is not the one under test", g.err)
	}
}

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
