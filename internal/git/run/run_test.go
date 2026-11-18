package run

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"ramus/internal/git/gitexe"
	"ramus/internal/git/gittest"
)

func runner(t *testing.T, max int) *Runner {
	t.Helper()
	g, err := gitexe.Find(context.Background(), os.Getenv("PATH"))
	if err != nil {
		t.Skip(err)
	}
	return New(g, append(gittest.Env(), "GIT_DIR=/wrong", "GIT_INDEX_FILE=/wrong/index"), max)
}

func TestRunNamesRepository(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a", "1")
	r.Commit("one")
	run := runner(t, 4)
	// The inherited GIT_DIR and GIT_INDEX_FILE must not win.
	res, err := run.Run(context.Background(), &Cmd{GitDir: r.GitDir(), WorkTree: r.Dir, Args: []string{"status", "--porcelain"}, Read: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Stdout) != 0 {
		t.Fatalf("status %q", res.Stdout)
	}
	if _, err := run.Run(context.Background(), &Cmd{Args: []string{"status"}}); err == nil {
		t.Fatal("ran a command with no repository")
	}
}

// A hardened runner refuses a write however it is started: through Run,
// or a long-lived process from Command, which then never starts.
func TestHardenedRefusesWrites(t *testing.T) {
	r := gittest.Init(t)
	r.Commit("one")
	h := runner(t, 1).Hardened(nil)
	write := &Cmd{GitDir: r.GitDir(), WorkTree: r.Dir, Args: []string{"commit", "--allow-empty", "-m", "two"}}
	if _, err := h.Run(context.Background(), write); !errors.Is(err, ErrRestricted) {
		t.Fatalf("Run: %v", err)
	}
	cmd := h.Command(context.Background(), write)
	if err := cmd.Run(); !errors.Is(err, ErrRestricted) {
		t.Fatalf("Command: %v", err)
	}
	if cmd.Process != nil {
		t.Fatal("a refused command started")
	}
	if n := strings.Count(r.Git("log", "--format=%s"), "\n"); n != 1 {
		t.Fatalf("%d commits", n)
	}
	read := h.Command(context.Background(), &Cmd{GitDir: r.GitDir(), Read: true, Args: []string{"rev-parse", "HEAD"}})
	if out, err := read.Output(); err != nil || len(strings.TrimSpace(string(out))) != 40 {
		t.Fatalf("a read: %q %v", out, err)
	}
}

func TestErrorKeepsStderr(t *testing.T) {
	r := gittest.Init(t)
	_, err := runner(t, 4).Run(context.Background(), &Cmd{GitDir: r.GitDir(), Args: []string{"rev-parse", "--verify", "nope"}})
	var e *Error
	if !errors.As(err, &e) || e.ExitCode != 128 || !strings.Contains(e.Stderr, "fatal") {
		t.Fatalf("got %#v", err)
	}
	if !strings.Contains(e.Error(), "git --git-dir=") {
		t.Fatalf("error does not name the command: %v", e)
	}
}

func TestTimeoutAndCancel(t *testing.T) {
	r := gittest.Init(t)
	run := runner(t, 4)
	// cat-file --batch waits on stdin forever.
	pr, pw, _ := os.Pipe()
	defer pw.Close()
	start := time.Now()
	_, err := run.Run(context.Background(), &Cmd{GitDir: r.GitDir(), Args: []string{"cat-file", "--batch"}, Stdin: pr, Timeout: 200 * time.Millisecond})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("got %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	_, err = run.Run(ctx, &Cmd{GitDir: r.GitDir(), Args: []string{"cat-file", "--batch"}, Stdin: pr})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if run.Running() != 0 {
		t.Fatalf("%d processes left", run.Running())
	}
}

func TestPoolBound(t *testing.T) {
	r := gittest.Init(t)
	run := runner(t, 2)
	peak := 0
	stop := make(chan struct{})
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		for {
			select {
			case <-stop:
				return
			default:
			}
			peak = max(peak, run.Running())
			time.Sleep(time.Millisecond)
		}
	}()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pr, pw, _ := os.Pipe()
			go func() { time.Sleep(50 * time.Millisecond); pw.Close() }()
			run.Run(context.Background(), &Cmd{GitDir: r.GitDir(), Args: []string{"cat-file", "--batch"}, Stdin: pr})
		}()
	}
	wg.Wait()
	close(stop)
	<-sampled
	if peak == 0 || peak > 2 {
		t.Fatalf("peak of %d processes with a limit of 2", peak)
	}
}

func TestStderrCapped(t *testing.T) {
	c := &capped{max: 4}
	c.Write([]byte("abcdef"))
	c.Write([]byte("gh"))
	if c.String() != "abcd\n[output cut]" {
		t.Fatalf("%q", c.String())
	}
}

// A user whose git speaks another language still gets messages the engine
// can read, where it reads them.
func TestUntranslated(t *testing.T) {
	r := gittest.Init(t)
	g, err := gitexe.Find(context.Background(), os.Getenv("PATH"))
	if err != nil {
		t.Skip(err)
	}
	run := New(g, append(gittest.Env(), "LC_ALL=zh_CN.UTF-8", "LANGUAGE=zh_CN"), 2)
	stderr := func(untranslated bool) string {
		_, err := run.Run(context.Background(), &Cmd{GitDir: r.GitDir(), Args: []string{"rev-parse", "--verify", "nope"}, Untranslated: untranslated})
		var e *Error
		if !errors.As(err, &e) {
			t.Fatal(err)
		}
		return e.Stderr
	}
	if strings.HasPrefix(stderr(false), "fatal") {
		t.Skip("this git has no Chinese messages")
	}
	if s := stderr(true); !strings.HasPrefix(s, "fatal") {
		t.Errorf("untranslated stderr %q", s)
	}
}

func TestMaxOutput(t *testing.T) {
	r := gittest.Init(t)
	r.Write("big", strings.Repeat("x", 1<<20))
	r.Commit("one")
	_, err := runner(t, 2).Run(context.Background(), &Cmd{GitDir: r.GitDir(), Args: []string{"cat-file", "blob", "HEAD:big"}, Read: true, MaxOutput: 64 << 10})
	if !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("got %v", err)
	}
}

func TestStderrLive(t *testing.T) {
	r := gittest.Init(t)
	var live strings.Builder
	_, err := runner(t, 1).Run(context.Background(), &Cmd{GitDir: r.GitDir(), Read: true, Stderr: &live,
		Args: []string{"rev-parse", "--verify", "nope"}})
	var e *Error
	if !errors.As(err, &e) || e.Stderr == "" || live.String() != e.Stderr {
		t.Fatalf("live %q, returned %v", live.String(), err)
	}
}

// The prompt environment reaches a command that may ask, never a read or
// one marked NoPrompt; a shell alias prints what git ran with.
func TestPrompt(t *testing.T) {
	r := gittest.Init(t)
	run := runner(t, 2)
	run.SetPrompt([]string{"RAMUS_PROMPT_TEST=yes"}, nil)
	got := func(c Cmd) string {
		c.GitDir, c.WorkTree = r.GitDir(), r.Dir
		c.Config = []string{`alias.e=!echo "[$RAMUS_PROMPT_TEST]"`}
		c.Args = []string{"e"}
		res, err := run.Run(context.Background(), &c)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(res.Stdout))
	}
	if s := got(Cmd{}); s != "[yes]" {
		t.Errorf("write: %q", s)
	}
	if s := got(Cmd{Read: true}); s != "[]" {
		t.Errorf("read: %q", s)
	}
	if s := got(Cmd{NoPrompt: true}); s != "[]" {
		t.Errorf("no prompt: %q", s)
	}
}

// A command that prints nothing for Stall is stopped, one that keeps
// Cancelling stops what git started too, as a hook's shell and what
// that runs: one left running would hold the output open until WaitDelay,
// and on Windows the folder it runs in.
func TestCancelStopsChildren(t *testing.T) {
	r := gittest.Init(t)
	run := runner(t, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := run.Run(ctx, &Cmd{GitDir: r.GitDir(), WorkTree: r.Dir,
		Config: []string{"alias.s=!sleep 30; echo late"}, Args: []string{"s"}})
	if err == nil {
		t.Fatal("ran to the end")
	}
	if d := time.Since(start); d >= 2*time.Second {
		t.Fatalf("returned after %v: the child outlived git", d)
	}
}

// printing is not, and a question open to the user does not count.
func TestStall(t *testing.T) {
	r := gittest.Init(t)
	asking := false
	var mu sync.Mutex
	run := runner(t, 2)
	run.SetPrompt(nil, func() bool { mu.Lock(); defer mu.Unlock(); return asking })
	sh := func(script string, stall time.Duration) error {
		_, err := run.Run(context.Background(), &Cmd{GitDir: r.GitDir(), WorkTree: r.Dir, Stall: stall,
			Config: []string{"alias.s=!" + script}, Args: []string{"s"}})
		return err
	}
	start := time.Now()
	if err := sh("sleep 5", 300*time.Millisecond); !errors.Is(err, ErrStalled) {
		t.Fatalf("silent: %v", err)
	}
	if d := time.Since(start); d > 4*time.Second {
		t.Fatalf("stopped after %v", d)
	}
	// Printing every half second, for longer than the stall: the limit
	// leaves a slow machine's start of git and its shell room.
	if err := sh("for i in 1 2 3 4 5 6; do echo x; sleep 0.5; done", 2*time.Second); err != nil {
		t.Fatalf("printing: %v", err)
	}
	mu.Lock()
	asking = true
	mu.Unlock()
	if err := sh("sleep 1", 300*time.Millisecond); err != nil {
		t.Fatalf("asking: %v", err)
	}
}

// A runner from Own runs its commands with its config, which a
// command's own config wins over, and the runner it came from without.
func TestOwnConfig(t *testing.T) {
	r := gittest.Init(t)
	base := runner(t, 2)
	own := base.Own()
	own.SetConfig([]string{"ramus.test=own"})
	get := func(run *Runner, config ...string) string {
		res, _ := run.Run(context.Background(), &Cmd{GitDir: r.GitDir(), WorkTree: r.Dir, Read: true, Config: config,
			Args: []string{"config", "--get", "ramus.test"}})
		return strings.TrimSpace(string(res.Stdout))
	}
	if got := get(own); got != "own" {
		t.Fatalf("own runner: %q", got)
	}
	if got := get(own, "ramus.test=command"); got != "command" {
		t.Fatalf("with the command's own: %q", got)
	}
	if got := get(base); got != "" {
		t.Fatalf("the runner it came from: %q", got)
	}
	own.SetConfig(nil)
	if got := get(own); got != "" {
		t.Fatalf("cleared: %q", got)
	}
}
