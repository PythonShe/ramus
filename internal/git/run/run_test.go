package run

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"ramus/internal/git/gitexe"
	"ramus/internal/git/gittest"
)

// findGit is testrun.Git, which imports this package: it fails the test
// when there is no git, and never skips it.
func findGit(t *testing.T) *gitexe.Git {
	t.Helper()
	gittest.GitPath(t)
	var err error
	for range 3 {
		var g *gitexe.Git
		if g, err = gitexe.Find(context.Background(), os.Getenv("PATH")); err == nil {
			return g
		}
	}
	t.Fatalf("git cannot be found: %v", err)
	return nil
}

func runner(t *testing.T, max int) *Runner {
	t.Helper()
	g := findGit(t)
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
	if time.Since(start) > 30*time.Second {
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

// A deadline of the caller's own is reported as that, not as the
// runner's time limit and its length.
func TestCallerDeadline(t *testing.T) {
	r := gittest.Init(t)
	pr, pw, _ := os.Pipe()
	defer pw.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := runner(t, 1).Run(ctx, &Cmd{GitDir: r.GitDir(), Args: []string{"cat-file", "--batch"}, Stdin: pr})
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrTimeout) {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), DefaultTimeout.String()) {
		t.Fatalf("reported as the runner's limit: %v", err)
	}
}

// Quit stops the reads and the commands marked to end with the app (not
// on Windows, where git cannot remove its locks then), and lets a write
// the user started end, and one that only asks no question.
func TestQuit(t *testing.T) {
	r := gittest.Init(t)
	run := runner(t, 4)
	pr, pw, _ := os.Pipe()
	defer pw.Close()
	cmds := []*Cmd{
		{GitDir: r.GitDir(), Read: true, Args: []string{"cat-file", "--batch"}, Stdin: pr},
		{GitDir: r.GitDir(), NoPrompt: true, EndsWithApp: true, Args: []string{"cat-file", "--batch"}, Stdin: pr},
	}
	if stopsHard {
		cmds = cmds[:1]
	}
	errs := make(chan error, len(cmds))
	for _, c := range cmds {
		go func() { _, err := run.Run(context.Background(), c); errs <- err }()
	}
	kept := make(chan error, 1)
	go func() {
		// Asks no question, and is let end.
		_, err := run.Run(context.Background(), &Cmd{GitDir: r.GitDir(), WorkTree: r.Dir, NoPrompt: true,
			Config: []string{"alias.s=!sleep 1"}, Args: []string{"s"}})
		kept <- err
	}()
	defer func() {
		if err := <-kept; err != nil {
			t.Errorf("a command that only asks no question: %v", err)
		}
	}()
	write := make(chan error, 1)
	go func() {
		_, err := run.Run(context.Background(), &Cmd{GitDir: r.GitDir(), WorkTree: r.Dir,
			Config: []string{"alias.s=!sleep 1"}, Args: []string{"s"}})
		write <- err
	}()
	time.Sleep(200 * time.Millisecond)
	run.Quit()
	for range cmds {
		select {
		case err := <-errs:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("got %v", err)
			}
		case <-time.After(time.Minute):
			t.Fatal("a read went on after Quit")
		}
	}
	if err := <-write; err != nil {
		t.Fatalf("the write: %v", err)
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
	g := findGit(t)
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
	// Well short of the child's half minute.
	if d := time.Since(start); d >= 15*time.Second {
		t.Fatalf("returned after %v: the child outlived git", d)
	}
}

// A command that succeeded succeeds although something it left running,
// as a hook's background job, still holds its output open.
func TestBackgroundChildKeepsSuccess(t *testing.T) {
	r := gittest.Init(t)
	res, err := runner(t, 1).Run(context.Background(), &Cmd{GitDir: r.GitDir(), WorkTree: r.Dir,
		Config: []string{"alias.bg=!sleep 3 & echo done"}, Args: []string{"bg"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Stdout) != "done\n" {
		t.Fatalf("stdout %q", res.Stdout)
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
	if err := sh("sleep 30", 300*time.Millisecond); !errors.Is(err, ErrStalled) {
		t.Fatalf("silent: %v", err)
	}
	if d := time.Since(start); d > 15*time.Second {
		t.Fatalf("stopped after %v", d)
	}
	// Printing every half second, for longer than the stall: the limit
	// leaves a slow machine's start of git and its shell room.
	if err := sh("for i in 1 2 3 4 5 6 7 8 9 10 11 12; do echo x; sleep 0.5; done", 5*time.Second); err != nil {
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

// A hardening override whose key holds "=", as a driver the repository
// named "a=b", still switches that driver off; and the hardening still
// wins over a command's own config.
func TestHardeningKeyWithEquals(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the drivers are shell commands")
	}
	r := gittest.Init(t)
	marks := t.TempDir()
	mark := func(name string) string { return fmt.Sprintf("touch %q; ", filepath.Join(marks, name)) }
	r.Write(".gitattributes", "*.x filter=a=b diff=c=d\n")
	r.Write("a.x", "one\n")
	r.Commit("one")
	r.Git("config", "filter.a=b.process", mark("process")+"exit 1")
	r.Git("config", "filter.a=b.clean", mark("clean")+"cat")
	r.Git("config", "diff.c=d.textconv", mark("textconv")+"cat")
	r.Write("a.x", "two\n")
	h := runner(t, 2).Hardened([]string{"core.pager=cat",
		"filter.a=b.clean=", "filter.a=b.smudge=", "filter.a=b.process=", "filter.a=b.required=false",
		"diff.c=d.textconv=", "diff.c=d.command="})
	ctx := context.Background()
	for _, args := range [][]string{
		{"status", "--porcelain=v2", "-z"},
		{"log", "-p", "--", "a.x"},
		{"diff-files", "-p"},
		// A textconv blanked is a command git cannot run: blame, which
		// applies it unless told not to, would fail on the file.
		{"blame", "--porcelain", "--", "a.x"},
		{"blame", "--porcelain", "HEAD", "--", "a.x"},
	} {
		if _, err := h.Run(ctx, &Cmd{GitDir: r.GitDir(), WorkTree: r.Dir, Read: true, Args: args}); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if ran, _ := os.ReadDir(marks); len(ran) > 0 {
		t.Fatalf("the repository's driver ran: %v", ran)
	}
	res, err := h.Run(ctx, &Cmd{GitDir: r.GitDir(), WorkTree: r.Dir, Read: true, Config: []string{"core.pager=less"},
		Args: []string{"config", "core.pager"}})
	if err != nil || strings.TrimSpace(string(res.Stdout)) != "cat" {
		t.Fatalf("core.pager %q %v", res.Stdout, err)
	}
}

// A hardened diff of the work tree does not ask a submodule whether it is
// dirty, which would run git status there under the submodule's own
// config.
func TestHardenedDiffLeavesSubmodules(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the driver is a shell command")
	}
	marks := t.TempDir()
	sub := gittest.Init(t)
	sub.Write(".gitattributes", "*.s filter=subf\n")
	sub.Write("a.s", "one\n")
	sub.Commit("one")
	sup := gittest.Init(t)
	sup.Write("f", "x\n")
	sup.Commit("base")
	sup.Git("-c", "protocol.file.allow=always", "submodule", "add", "-q", sub.Dir, "sub")
	sup.Commit("sub")
	sup.Git("--git-dir="+filepath.Join(sup.GitDir(), "modules", "sub"), "config", "filter.subf.process",
		fmt.Sprintf("touch %q; exit 1", filepath.Join(marks, "sub-process")))
	if err := os.WriteFile(filepath.Join(sup.Dir, "sub", "a.s"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := runner(t, 2).Hardened([]string{"core.fsmonitor=false"})
	for _, args := range [][]string{
		{"diff-files", "--numstat", "-z", "--submodule=short"},
		{"diff-index", "--numstat", "-z", "HEAD"},
		{"diff", "--no-color", "--submodule=short"},
	} {
		if _, err := h.Run(context.Background(), &Cmd{GitDir: sup.GitDir(), WorkTree: sup.Dir, Read: true, Args: args}); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if ran, _ := os.ReadDir(marks); len(ran) > 0 {
			t.Fatalf("%v ran the submodule's driver", args)
		}
	}
}

// The settings of a user's global config that change what git prints for
// the engine to parse are undone for every read, and a command's own
// config still wins over that.
func TestReadIgnoresParseBreakingConfig(t *testing.T) {
	r := gittest.Init(t)
	gittest.SSHSigning(t, r)
	r.Write("a", "one\n\nthree\n")
	plain := r.Commit("plain")
	r.Write("b", "x\n")
	r.Git("add", "b")
	r.Git("commit", "-q", "-S", "-m", "signed")
	signed := strings.TrimSpace(r.Git("rev-parse", "HEAD"))
	r.Write("a", "one\n\nfour\n")
	r.Commit("again")
	r.Write("a", "one\n\n3\n")
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, []byte(`[log]
	showSignature = true
	follow = true
	showRoot = false
[color]
	ui = always
	diff = always
[diff]
	noprefix = true
	mnemonicPrefix = true
	srcPrefix = S/
	dstPrefix = D/
	suppressBlankEmpty = true
`), 0o644); err != nil {
		t.Fatal(err)
	}
	g := findGit(t)
	run := New(g, append(gittest.Env(), "GIT_CONFIG_GLOBAL="+global), 2)
	out := func(config []string, args ...string) string {
		t.Helper()
		res, err := run.Run(context.Background(), &Cmd{GitDir: r.GitDir(), WorkTree: r.Dir, Read: true, Config: config, Args: args})
		if err != nil {
			t.Fatal(err)
		}
		return string(res.Stdout)
	}
	if s := out(nil, "log", "-1", "--format=%B", signed, "--"); s != "signed\n\n" {
		t.Errorf("message of a signed commit: %q", s)
	}
	// Following renames, git would not rewrite the parents to the
	// commits the filter keeps.
	if s := out(nil, "log", "-1", "--format=%P", "--parents", "--", "a"); s != plain+"\n" {
		t.Errorf("parent in a one-path log: %q, want %s", s, plain)
	}
	if s := out(nil, "log", "--format=%s", "--name-only", plain); !strings.Contains(s, "\na\n") {
		t.Errorf("root commit: %q", s)
	}
	diff := out(nil, "diff")
	for _, want := range []string{"diff --git a/a b/a\n", "--- a/a\n", "+++ b/a\n", "\n \n-four\n"} {
		if !strings.Contains(diff, want) {
			t.Errorf("diff has no %q:\n%s", want, diff)
		}
	}
	if strings.Contains(diff, "\x1b[") {
		t.Errorf("diff in colour:\n%q", diff)
	}
	// Reading the config shows the user's, not the runner's.
	if s := out(nil, "config", "--get", "log.showSignature"); s != "true\n" {
		t.Errorf("config read: %q", s)
	}
	if s := out([]string{"log.showSignature=true"}, "log", "-1", "--format=%s", signed, "--"); !strings.Contains(s, "Good") {
		t.Errorf("the command's own config lost: %q", s)
	}
}
