// Package run starts git processes for the engine: never through a shell,
// always naming the repository, a bounded number at a time across every
// open repository, each cancellable and time-limited, with its standard
// error kept for the user to read.
package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	"ramus/internal/git/gitexe"
)

// DefaultTimeout bounds a command that sets no Timeout of its own.
const DefaultTimeout = 2 * time.Minute

// stderrLimit bounds the standard error kept per command.
const stderrLimit = 64 << 10

// localEnv are the variables that point git at a repository (git
// rev-parse --local-env-vars). One inherited from wherever Ramus was
// started, a hook's environment say, would send every command to the
// wrong repository, so they are dropped.
var localEnv = map[string]bool{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": true, "GIT_CONFIG": true, "GIT_CONFIG_PARAMETERS": true,
	"GIT_CONFIG_COUNT": true, "GIT_OBJECT_DIRECTORY": true, "GIT_DIR": true, "GIT_WORK_TREE": true,
	"GIT_IMPLICIT_WORK_TREE": true, "GIT_GRAFT_FILE": true, "GIT_INDEX_FILE": true,
	"GIT_NO_REPLACE_OBJECTS": true, "GIT_REPLACE_REF_BASE": true, "GIT_PREFIX": true,
	"GIT_SHALLOW_FILE": true, "GIT_COMMON_DIR": true,
}

// Runner runs git. One Runner serves the whole app, so its limit on
// concurrent processes holds across every repository.
type Runner struct {
	git *gitexe.Git
	env []string
	*pool
	// hardening is set on a runner for a repository that is not trusted.
	hardening []string
}

// pool is shared by a runner and the hardened runners made from it.
type pool struct {
	slots chan struct{}
	// running counts processes alive, for the soak test and diagnostics.
	running atomic.Int64
}

// New returns a runner for git with env (the resolved user environment)
// that runs at most max processes at once.
func New(git *gitexe.Git, env []string, max int) *Runner {
	var clean []string
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if !localEnv[k] {
			clean = append(clean, kv)
		}
	}
	return &Runner{git: git, env: clean, pool: &pool{slots: make(chan struct{}, max)}}
}

// Hardened returns a runner, sharing this one's process limit, for a
// repository that is not trusted: config adds -c overrides after every
// command's own, so they win; diff, log and show run no external diff and
// no textconv; and any command not marked Read is refused, so nothing
// writes.
func (r *Runner) Hardened(config []string) *Runner {
	return &Runner{git: r.git, env: r.env, pool: r.pool, hardening: append([]string{}, config...)}
}

// IsHardened reports a runner from Hardened.
func (r *Runner) IsHardened() bool { return r.hardening != nil }

// DefaultMaxOutput bounds collected output: far over what a command the
// engine reads whole prints for any real repository (the raw file list of
// a commit touching every file of a 400,000-file tree is some 60 MB).
const DefaultMaxOutput = 512 << 20

// ErrOutputTooLarge is returned when a command prints past MaxOutput.
var ErrOutputTooLarge = errors.New("run: output too large")

// ErrRestricted is returned for a write through a hardened runner.
var ErrRestricted = errors.New("run: the repository is not trusted, so git may only read")

// Git returns the executable the runner starts.
func (r *Runner) Git() *gitexe.Git { return r.git }

// Running returns the number of processes alive.
func (r *Runner) Running() int { return int(r.running.Load()) }

// Cmd is one git command.
type Cmd struct {
	// GitDir is the repository's git directory; required.
	GitDir string
	// WorkTree is its work tree, empty for a bare repository or a command
	// that needs none. The process runs in it, so pathspecs are relative
	// to it.
	WorkTree string
	Args     []string
	// Read marks a command that only reads: it takes no optional locks, so
	// it never rewrites the index behind the user's back, and fetches
	// nothing from a promisor remote.
	Read bool
	// Config is passed as -c key=value before the command.
	Config []string
	Env    []string
	Stdin  io.Reader
	// Stdout receives the output as it comes; when nil, Run returns it.
	Stdout io.Writer
	// Stderr, when set, also receives git's messages as they come (progress,
	// hook output); Run returns them either way.
	Stderr io.Writer
	// Timeout bounds the command; DefaultTimeout when zero.
	Timeout time.Duration
	// MaxOutput bounds the output Run collects when Stdout is nil;
	// DefaultMaxOutput when zero. Past it git is stopped and Run returns
	// ErrOutputTooLarge.
	MaxOutput int
	// Discover runs git in WorkTree, or GitDir when there is none, and lets
	// it find the repository itself instead of naming it, so that git's
	// discovery checks (ownership, safe.bareRepository) apply; naming the
	// git directory skips them.
	Discover bool
	// Outside runs git with no repository, in WorkTree: clone and init.
	Outside bool
	// Untranslated makes git print its messages in English, for a command
	// whose stderr the engine reads. Other commands keep the user's
	// language, since their errors are shown as they are.
	Untranslated bool
}

// Line returns the command as a user would type it, for the command log
// and error messages.
func (c *Cmd) Line() string { return "git " + strings.Join(c.argv(), " ") }

func (c *Cmd) argv() []string {
	var a []string
	if !c.Discover && !c.Outside {
		a = append(a, "--git-dir="+c.GitDir)
		if c.WorkTree != "" {
			a = append(a, "--work-tree="+c.WorkTree)
		}
	}
	if c.Read {
		// What the app reads it reads as UTF-8; git otherwise writes
		// messages in i18n.commitEncoding when one is set.
		a = append(a, "--no-optional-locks", "-c", "i18n.logOutputEncoding=UTF-8")
	}
	for _, kv := range c.Config {
		a = append(a, "-c", kv)
	}
	return append(a, c.Args...)
}

// argv adds the hardening of a runner for a repository not trusted.
func (r *Runner) argv(c *Cmd) []string {
	a := c.argv()
	if r.hardening == nil {
		return a
	}
	globals := a[: len(a)-len(c.Args) : len(a)-len(c.Args)]
	for _, kv := range r.hardening {
		globals = append(globals, "-c", kv)
	}
	if len(c.Args) == 0 {
		return globals
	}
	globals = append(globals, c.Args[0])
	switch c.Args[0] {
	case "diff", "log", "show":
		globals = append(globals, "--no-ext-diff", "--no-textconv")
	}
	return append(globals, c.Args[1:]...)
}

// Result is what a finished command produced.
type Result struct {
	Stdout   []byte
	Stderr   string
	Duration time.Duration
}

// Error is a command that failed: its exit code and its standard error as
// git wrote it.
type Error struct {
	Line     string
	ExitCode int // -1 when it did not exit on its own
	Stderr   string
	Err      error
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = e.Err.Error()
	}
	return e.Line + ": " + msg
}

func (e *Error) Unwrap() error { return e.Err }

// ErrTimeout is wrapped by the error of a command that ran past its time.
var ErrTimeout = errors.New("run: timed out")

// Run runs c and waits for it, after waiting for a free slot. Cancelling
// ctx stops the wait or the process.
func (r *Runner) Run(ctx context.Context, c *Cmd) (Result, error) {
	if c.GitDir == "" && !(c.Outside && c.WorkTree != "") {
		return Result{}, errors.New("run: command names no repository")
	}
	if r.hardening != nil && !c.Read {
		return Result{}, &Error{Line: c.Line(), ExitCode: -1, Err: ErrRestricted}
	}
	select {
	case r.slots <- struct{}{}:
	case <-ctx.Done():
		return Result{}, &Error{Line: c.Line(), ExitCode: -1, Err: ctx.Err()}
	}
	defer func() { <-r.slots }()
	return r.start(ctx, c)
}

// Command prepares a long-lived process, such as cat-file --batch-command,
// that lives as long as its repository: it takes no slot of the pool and
// has no time limit, and stops when ctx is cancelled. Track tells the
// runner it started and ended, for Running.
func (r *Runner) Command(ctx context.Context, c *Cmd) *exec.Cmd {
	cmd := r.command(ctx, c)
	cmd.WaitDelay = 2 * time.Second
	return cmd
}

// Track counts a process started from Command until done is called.
func (r *Runner) Track() (done func()) {
	r.running.Add(1)
	var once atomic.Bool
	return func() {
		if once.CompareAndSwap(false, true) {
			r.running.Add(-1)
		}
	}
}

func (r *Runner) command(ctx context.Context, c *Cmd) *exec.Cmd {
	cmd := exec.CommandContext(ctx, r.git.Path, r.argv(c)...)
	cmd.Dir = c.WorkTree
	if cmd.Dir == "" {
		cmd.Dir = c.GitDir
	}
	cmd.Env = append(append([]string(nil), r.env...), c.Env...)
	if c.Read {
		cmd.Env = append(cmd.Env, "GIT_NO_LAZY_FETCH=1")
	}
	cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0")
	if c.Untranslated {
		// LC_ALL would override LC_MESSAGES; the C locale ignores LANGUAGE.
		// LANG and LC_CTYPE stay, so paths and text decode as before.
		cmd.Env = append(cmd.Env, "LC_ALL=", "LC_MESSAGES=C")
	}
	cmd.Stdin = c.Stdin
	stopGroup(cmd)
	return cmd
}

func (r *Runner) start(ctx context.Context, c *Cmd) (Result, error) {
	timeout := c.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := r.command(ctx, c)
	limit := c.MaxOutput
	if limit <= 0 {
		limit = DefaultMaxOutput
	}
	stdout := &bounded{max: limit, cancel: cancel}
	if c.Stdout != nil {
		cmd.Stdout = c.Stdout
	} else {
		cmd.Stdout = stdout
	}
	stderr := &capped{max: stderrLimit}
	cmd.Stderr = stderr
	if c.Stderr != nil {
		cmd.Stderr = io.MultiWriter(stderr, c.Stderr)
	}
	cmd.WaitDelay = 2 * time.Second
	t0 := time.Now()
	r.running.Add(1)
	err := cmd.Run()
	r.running.Add(-1)
	res := Result{Stdout: stdout.buf, Stderr: stderr.String(), Duration: time.Since(t0)}
	if err != nil {
		e := &Error{Line: c.Line(), ExitCode: -1, Stderr: res.Stderr, Err: err}
		var exit *exec.ExitError
		switch {
		case stdout.over:
			e.Err = fmt.Errorf("%w: over %d bytes", ErrOutputTooLarge, limit)
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			e.Err = fmt.Errorf("%w after %v", ErrTimeout, timeout)
		case ctx.Err() != nil:
			e.Err = ctx.Err()
		case errors.As(err, &exit):
			e.ExitCode = exit.ExitCode()
		}
		return res, e
	}
	return res, nil
}

// capped keeps the first max bytes written to it.
// bounded collects output up to max bytes and then stops the command.
type bounded struct {
	buf    []byte
	max    int
	over   bool
	cancel func()
}

func (b *bounded) Write(p []byte) (int, error) {
	if len(b.buf)+len(p) > b.max {
		b.over = true
		b.cancel()
		return 0, ErrOutputTooLarge
	}
	b.buf = append(b.buf, p...)
	return len(p), nil
}

type capped struct {
	buf bytes.Buffer
	max int
	cut bool
}

func (c *capped) Write(b []byte) (int, error) {
	n := len(b)
	if room := c.max - c.buf.Len(); room < len(b) {
		b = b[:max(room, 0)]
		c.cut = true
	}
	c.buf.Write(b)
	return n, nil
}

func (c *capped) String() string {
	if c.cut {
		return c.buf.String() + "\n[output cut]"
	}
	return c.buf.String()
}
