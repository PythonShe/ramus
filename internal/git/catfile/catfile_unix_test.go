//go:build unix

package catfile

import (
	"bufio"
	"bytes"
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

// A git that stalls and cannot be ended, as one in uninterruptible sleep
// on a hung network volume, neither holds the read nor Close: here a
// process no signal reaches holds the pipes open until the test lets it go.
func TestStalledGitThatCannotDie(t *testing.T) {
	hold, release, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Close()
	defer release.Close()
	// It reads only from the pipe the test holds, answers nothing, and
	// exits when the test closes that pipe.
	cmd := exec.Command("/bin/sh", "-c", "read x <&3")
	cmd.ExtraFiles = []*os.File{hold}
	cmd.WaitDelay = time.Hour
	g := &proc{cmd: cmd, cancel: func() {}, untrack: func() {}, stderr: &limitedWriter{n: 1 << 10}}
	cmd.Stderr = g.stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	g.stdin, g.stdout = stdin, stdout
	g.in, g.out = bufio.NewWriter(stdin), bufio.NewReader(stdout)
	p := &Process{batch: make(chan struct{}, 1), g: g, idle: time.Hour, stall: 100 * time.Millisecond}

	done := make(chan error, 1)
	go func() { done <- p.Read([][]byte{bytes.Repeat([]byte{1}, 20)}, func(int, Object) error { return nil }) }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrClosed) || !strings.Contains(err.Error(), run.ErrStalled.Error()) {
			t.Fatalf("stalled read: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a git that cannot die holds the read")
	}
	closed := make(chan error, 1)
	go func() { closed <- p.Close() }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close waits for a git that cannot die")
	}
}
