//go:build unix

package catfile

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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

// While a git that stopped answering has not exited, no other starts for
// its repository, which on a hung volume would hang in turn, one more
// process each stall: reads fail at once, saying why. Once it exits, the
// next read starts git again.
func TestNoGitStartsWhileOneCannotDie(t *testing.T) {
	r := gittest.Init(t)
	c := r.Commit("first")
	runner := testrun.Runner(t)
	gitDir, err := filepath.Abs(r.GitDir())
	if err != nil {
		t.Fatal(err)
	}
	hold, release, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Close()
	defer release.Close()
	// A stand-in for a git in uninterruptible sleep: it answers nothing,
	// ignores the end of its input and its cancel, and exits when the
	// test closes the pipe it reads.
	cmd := exec.Command("/bin/sh", "-c", "read x <&3")
	cmd.ExtraFiles = []*os.File{hold}
	g := &proc{cmd: cmd, cancel: func() {}, untrack: func() {}, stderr: &limitedWriter{n: 1 << 10}, done: make(chan struct{})}
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
	p := &Process{ctx: context.Background(), runner: runner, gitDir: gitDir, batch: make(chan struct{}, 1), g: g, idle: time.Hour, stall: 100 * time.Millisecond}
	defer p.Close()

	read := func(p *Process) error {
		done := make(chan error, 1)
		go func() { done <- p.Read([][]byte{decode(c)}, func(int, Object) error { return nil }) }()
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			t.Fatal("the read is held")
			return nil
		}
	}
	if err := read(p); !strings.Contains(err.Error(), run.ErrStalled.Error()) {
		t.Fatalf("stalled read: %v", err)
	}
	before := runner.Running()
	if q, err := Start(context.Background(), runner, gitDir); !errors.Is(err, run.ErrStalled) {
		if q != nil {
			q.Close()
		}
		t.Fatalf("a new reader started beside the git that cannot die: %v", err)
	}
	if n := runner.Running(); n != before {
		t.Fatalf("%d gits running, %d before", n, before)
	}

	release.Close()
	select {
	case <-g.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the stand-in did not exit")
	}
	q, err := Start(context.Background(), runner, gitDir)
	if err != nil {
		t.Fatalf("no reader once the stuck git exited: %v", err)
	}
	defer q.Close()
	if err := read(q); err != nil {
		t.Fatalf("read once the stuck git exited: %v", err)
	}
}

// A git let go of by a cancelled read that has not exited when Close
// gives up on it is counted as stuck, as the running one would be: no
// other starts for its repository until it exits.
func TestGitLetGoOfThatDoesNotExitHoldsStartsOff(t *testing.T) {
	defer func(d time.Duration) { closeWait = d }(closeWait)
	closeWait = 500 * time.Millisecond
	r := gittest.Init(t)
	c := r.Commit("first")
	runner := testrun.Runner(t)
	gitDir, err := filepath.Abs(r.GitDir())
	if err != nil {
		t.Fatal(err)
	}
	hold, release, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Close()
	defer release.Close()
	// A stand-in for a git killed while it cannot finish exiting: on a
	// line from the test it closes its pipes, as a dying process does
	// first, and then stays until the test closes the pipe it reads.
	cmd := exec.Command("/bin/sh", "-c", "read x <&3; exec 0<&- 1>&- 2>&-; read y <&3")
	cmd.ExtraFiles = []*os.File{hold}
	g := &proc{cmd: cmd, cancel: func() {}, untrack: func() {}, stderr: &limitedWriter{n: 1 << 10}, done: make(chan struct{})}
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
	p := &Process{ctx: context.Background(), runner: runner, gitDir: gitDir, batch: make(chan struct{}, 1), g: g, idle: time.Hour, stall: time.Hour}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.ReadContext(ctx, [][]byte{decode(c)}, func(int, Object) error { return nil }) }()
	// Cancelled once the read has its turn and waits for its answer.
	for len(p.batch) == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	cancel()
	release.Write([]byte("go\n"))
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled read: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the read is held")
	}
	p.mu.Lock()
	running := p.g
	p.mu.Unlock()
	if running != nil {
		t.Fatal("the cancelled read kept its git")
	}
	if err := p.Close(); err == nil {
		t.Fatal("Close says the git that has not exited ended")
	}
	if q, err := Start(context.Background(), runner, gitDir); !errors.Is(err, run.ErrStalled) {
		if q != nil {
			q.Close()
		}
		t.Fatalf("a new reader started beside the git that has not exited: %v", err)
	}

	release.Close()
	select {
	case <-g.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the stand-in did not exit")
	}
	q, err := Start(context.Background(), runner, gitDir)
	if err != nil {
		t.Fatalf("no reader once the git exited: %v", err)
	}
	q.Close()
}
