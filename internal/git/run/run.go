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
	git   *gitexe.Git
	env   []string
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
	return &Runner{git: git, env: clean, slots: make(chan struct{}, max)}
}

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
	// Timeout bounds the command; DefaultTimeout when zero.
	Timeout time.Duration
}

// Line returns the command as a user would type it, for the command log
// and error messages.
func (c *Cmd) Line() string { return "git " + strings.Join(c.argv(), " ") }

func (c *Cmd) argv() []string {
	a := []string{"--git-dir=" + c.GitDir}
	if c.WorkTree != "" {
		a = append(a, "--work-tree="+c.WorkTree)
	}
	if c.Read {
		a = append(a, "--no-optional-locks")
	}
	for _, kv := range c.Config {
		a = append(a, "-c", kv)
	}
	return append(a, c.Args...)
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
	if c.GitDir == "" {
		return Result{}, errors.New("run: command names no repository")
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
	cmd := exec.CommandContext(ctx, r.git.Path, c.argv()...)
	cmd.Dir = c.WorkTree
	if cmd.Dir == "" {
		cmd.Dir = c.GitDir
	}
	cmd.Env = append(append([]string(nil), r.env...), c.Env...)
	if c.Read {
		cmd.Env = append(cmd.Env, "GIT_NO_LAZY_FETCH=1")
	}
	cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0")
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
	var stdout bytes.Buffer
	if c.Stdout != nil {
		cmd.Stdout = c.Stdout
	} else {
		cmd.Stdout = &stdout
	}
	stderr := &capped{max: stderrLimit}
	cmd.Stderr = stderr
	cmd.WaitDelay = 2 * time.Second
	t0 := time.Now()
	r.running.Add(1)
	err := cmd.Run()
	r.running.Add(-1)
	res := Result{Stdout: stdout.Bytes(), Stderr: stderr.String(), Duration: time.Since(t0)}
	if err != nil {
		e := &Error{Line: c.Line(), ExitCode: -1, Stderr: res.Stderr, Err: err}
		var exit *exec.ExitError
		switch {
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
