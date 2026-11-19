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
	"runtime"
	"slices"
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

// readConfig is config every command that only reads runs with, through
// the environment (GIT_CONFIG_COUNT), so that it wins over the user's
// files and loses to any -c, a command's own included. It undoes the
// settings of a user's global or system config that change the bytes the
// engine parses: signature checks printed before each commit, a one-path
// log turned into --follow, a root commit shown with no changes, colour
// codes, and patch paths without the a/ and b/ every patch reader and git
// apply expect, or with blank context lines left empty. What a user
// chooses a diff to show (algorithm, renames, context, textconv) stays
// theirs.
var readConfig = [][2]string{
	{"log.showSignature", "false"},
	{"log.follow", "false"},
	{"log.showRoot", "true"},
	{"color.ui", "never"},
	{"color.diff", "never"},
	{"diff.noprefix", "false"},
	{"diff.mnemonicPrefix", "false"},
	{"diff.srcPrefix", "a/"},
	{"diff.dstPrefix", "b/"},
	{"diff.relative", "false"},
	{"diff.submodule", "short"},
	{"diff.suppressBlankEmpty", "false"},
}

// readEnv is readConfig as variables.
var readEnv = func() []string {
	env := []string{fmt.Sprintf("GIT_CONFIG_COUNT=%d", len(readConfig))}
	for i, kv := range readConfig {
		env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, kv[0]), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, kv[1]))
	}
	return env
}()

// Runner runs git. One Runner serves the whole app, so its limit on
// concurrent processes holds across every repository.
type Runner struct {
	git *gitexe.Git
	env []string
	*pool
	// hardened is set on a runner for a repository that is not trusted;
	// hardening is its overrides, replaced when the config changes.
	hardened  bool
	hardening atomic.Pointer[[]string]
	// prompt is the environment that lets a command ask the user, as
	// for a password, and asking reports a question open; see SetPrompt.
	prompt []string
	asking func() bool
	// config is the -c overrides of a runner from Own; see SetConfig.
	config atomic.Pointer[[]string]
}

// pool is shared by a runner and the hardened runners made from it.
type pool struct {
	slots chan struct{}
	// running counts processes alive, for the soak test and diagnostics;
	// commands those of them Run started.
	running  atomic.Int64
	commands atomic.Int64
	journal  Journal
	// writes counts the writes running per repository, where stopping
	// one leaves its locks for the runner to remove (stopsHard).
	writes writes
	// quit is cancelled when the app quits; see Quit.
	quit     context.Context
	quitting context.CancelFunc
}

// New returns a runner for git with env (the resolved user environment)
// that runs at most max processes at once.
func New(git *gitexe.Git, env []string, max int) *Runner {
	var clean []string
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if runtime.GOOS == "windows" {
			k = strings.ToUpper(k) // git there reads Git_Dir as GIT_DIR
		}
		// Without GIT_CONFIG_COUNT these do nothing, and with the count
		// readEnv sets they would add to its entries.
		if !localEnv[k] && !strings.HasPrefix(k, "GIT_CONFIG_KEY_") && !strings.HasPrefix(k, "GIT_CONFIG_VALUE_") {
			clean = append(clean, kv)
		}
	}
	p := &pool{slots: make(chan struct{}, max)}
	p.quit, p.quitting = context.WithCancel(context.Background())
	return &Runner{git: git, env: clean, pool: p}
}

// Environ is the user's environment commands run with, for other tools
// of theirs.
func (r *Runner) Environ() []string { return slices.Clone(r.env) }

// SetPrompt has every command that is not Read nor NoPrompt run with
// env, the app's askpass helper, so that git and ssh can ask the user;
// asking, when set, reports whether a question is open, during which no
// command stalls. Call it before the first command. A runner from
// Hardened, which runs reads only, never asks.
func (r *Runner) SetPrompt(env []string, asking func() bool) {
	r.prompt, r.asking = slices.Clone(env), asking
}

// Hardened returns a runner, sharing this one's process limit, for a
// repository that is not trusted: config, as key=value with the value
// after the last "=", adds overrides after every command's own, so they
// win; diff, log and show run no external diff and no textconv; diff asks
// no submodule whether it is dirty; and any command not marked Read is
// refused, so nothing writes.
func (r *Runner) Hardened(config []string) *Runner {
	h := &Runner{git: r.git, env: r.env, pool: r.pool, hardened: true}
	h.Reharden(config)
	return h
}

// Reharden replaces the overrides of a runner from Hardened, for config
// that changed: commands started after it returns take them.
func (r *Runner) Reharden(config []string) {
	if r.hardened {
		c := append([]string{}, config...)
		r.hardening.Store(&c)
	}
}

// Own returns a runner like this one, sharing its process limit and
// journal, for one repository: SetConfig on it changes none of the
// others'.
func (r *Runner) Own() *Runner {
	o := &Runner{git: r.git, env: r.env, pool: r.pool, prompt: r.prompt, asking: r.asking, hardened: r.hardened}
	if h := r.hardening.Load(); h != nil {
		o.hardening.Store(h)
	}
	return o
}

// SetConfig has every command started after it returns run with config
// as -c overrides before its own, which win over them, as a hardened
// runner's overrides win over both. Safe from any goroutine.
func (r *Runner) SetConfig(config []string) {
	c := slices.Clone(config)
	r.config.Store(&c)
}

// Quit stops every command Run is running that reads or is marked
// EndsWithApp, for an app that quits: git would go on without it, a
// fetch hung on the network for hours. A write the user started is let
// end, and so is a process from Command. Such a command Run starts after
// Quit is stopped at once: nothing should run once the app quits.
func (r *Runner) Quit() { r.quitting() }

// IsHardened reports a runner from Hardened.
func (r *Runner) IsHardened() bool { return r.hardened }

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

// Journal returns the record of the commands the runner started.
func (r *Runner) Journal() *Journal { return &r.journal }

// Running returns the number of processes alive.
func (r *Runner) Running() int { return int(r.running.Load()) }

// Commands returns the number of commands Run started that have not
// ended: Running without the processes that live as long as their
// repository.
func (r *Runner) Commands() int { return int(r.commands.Load()) }

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
	// NoPrompt keeps git and ssh from asking the user through the app,
	// for a command no one is there to answer, as a background fetch: it
	// fails instead. It says nothing of when the command ends.
	NoPrompt bool
	// EndsWithApp stops the command when the app quits or ends, for one
	// no one waits on, as a background fetch, which could otherwise hang
	// on the network for hours. A read always ends with the app. On
	// Windows, where git stopped from outside cannot remove its lock
	// files, only a read does: the rest runs to its end, as it would
	// from a terminal.
	EndsWithApp bool
	// Stall stops the command once it has printed nothing for that long,
	// for a network command whose server or connection stopped
	// answering; 0 never. Time a question to the user is open does not
	// count (see SetPrompt).
	Stall time.Duration
	// Background runs git at a low priority, for work no one waits on
	// that takes a while, as a commit-graph the app writes on its own.
	Background bool
}

// Line returns the command as a user would type it, for the command log
// and error messages. The runner's own overrides (readConfig, and a
// hardened runner's) are not part of it.
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

// MaxArgBytes bounds the arguments of a command that names paths git
// reads only as arguments: Windows limits a command line to 32767
// characters, git's own path and the runner's config included.
const MaxArgBytes = 24 << 10

// ArgBytes is what a takes of a command line, its separator included,
// as Windows is given it: Go quotes an argument that is empty or holds a
// space, a tab or a quote, escaping each quote and the backslashes before
// one with a backslash. The count is over, never under: UTF-8 takes at
// least as many bytes as UTF-16 takes characters.
func ArgBytes(a string) int {
	n := len(a) + 1
	if a == "" || strings.ContainsAny(a, " \t\"") {
		n += 2 + strings.Count(a, `"`) + strings.Count(a, `\`)
	}
	return n
}

// LineBytes is what c's arguments take of a command line, as ArgBytes
// counts them; the runner's own config is left to MaxArgBytes' margin.
func (c *Cmd) LineBytes() int {
	n := 0
	for _, a := range c.argv() {
		n += ArgBytes(a)
	}
	return n
}

// argv adds the runner's own config, and the hardening of a runner for
// a repository not trusted; env holds the variables the hardening names.
func (r *Runner) argv(c *Cmd) (args, env []string) {
	if own := r.config.Load(); own != nil && len(*own) > 0 {
		cc := *c
		cc.Config = append(slices.Clone(*own), c.Config...)
		c = &cc
	}
	a := c.argv()
	if !r.hardened {
		return a, nil
	}
	globals := a[: len(a)-len(c.Args) : len(a)-len(c.Args)]
	for i, kv := range *r.hardening.Load() {
		// git splits -c at the first "=", so a driver the repository
		// named "a=b" would turn -c filter.a=b.process= into the key
		// filter.a and leave its driver in force. --config-env splits at
		// the last "=", and the value comes from the environment; it
		// keeps -c's place on the command line, so the hardening still
		// wins over every -c before it.
		k, v, ok := cutLast(kv, "=")
		if !ok {
			globals = append(globals, "-c", kv)
			continue
		}
		name := fmt.Sprintf("RAMUS_HARDENING_%d", i)
		globals = append(globals, "--config-env="+k+"="+name)
		env = append(env, name+"="+v)
	}
	if len(c.Args) == 0 {
		return globals, env
	}
	globals = append(globals, c.Args[0])
	switch c.Args[0] {
	case "diff", "log", "show":
		globals = append(globals, "--no-ext-diff", "--no-textconv")
	case "blame":
		// The hardening blanks the repository's textconv drivers, which
		// blame would then fail to run on the file.
		globals = append(globals, "--no-textconv")
	}
	switch c.Args[0] {
	case "diff", "diff-files", "diff-index":
		// Asking whether a submodule's work tree is dirty runs git status
		// inside it, under the submodule's own config, which the hardening,
		// made from the superproject's, does not cover.
		globals = append(globals, "--ignore-submodules=dirty")
	}
	return append(globals, c.Args[1:]...), env
}

// cutLast is strings.Cut at the last sep: the hardening's values hold no
// "=", its keys may.
func cutLast(s, sep string) (before, after string, found bool) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+len(sep):], true
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

// ErrStalled is returned for a command stopped by Cmd.Stall.
var ErrStalled = errors.New("run: stalled")

// Run runs c and waits for it, after waiting for a free slot. Cancelling
// ctx stops the wait or the process.
func (r *Runner) Run(ctx context.Context, c *Cmd) (Result, error) {
	if c.GitDir == "" && !(c.Outside && c.WorkTree != "") {
		return Result{}, errors.New("run: command names no repository")
	}
	if r.hardened && !c.Read {
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
// has no time limit, and stops when ctx is cancelled. Start starts it;
// Track tells the runner it started and ended, for Running. A write
// through a hardened runner fails to start, with ErrRestricted, as Run
// refuses it.
func (r *Runner) Command(ctx context.Context, c *Cmd) *exec.Cmd {
	if r.hardened && !c.Read {
		// Start returns a Cmd's Err without running anything, so each
		// caller's own error path reports the refusal.
		cmd := exec.CommandContext(ctx, r.git.Path)
		cmd.Err = &Error{Line: c.Line(), ExitCode: -1, Err: ErrRestricted}
		return cmd
	}
	cmd := r.command(ctx, c)
	cmd.WaitDelay = 2 * time.Second
	r.journal.record(c, c.Line(), time.Now(), Result{}, nil, true)
	return cmd
}

// Start starts a process Command prepared in the group that cancelling
// it stops, with everything it starts.
func (r *Runner) Start(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		leaveGroup(cmd)
		return err
	}
	joinGroup(cmd)
	return nil
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
	args, hardening := r.argv(c)
	cmd := exec.CommandContext(ctx, r.git.Path, args...)
	cmd.Dir = c.WorkTree
	if cmd.Dir == "" {
		cmd.Dir = c.GitDir
	}
	cmd.Env = append(append(append([]string(nil), r.env...), c.Env...), hardening...)
	if c.Read {
		cmd.Env = append(cmd.Env, "GIT_NO_LAZY_FETCH=1")
		// git config prints none of it, and would list it as the
		// repository's config.
		if len(c.Args) == 0 || c.Args[0] != "config" {
			cmd.Env = append(cmd.Env, readEnv...)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0")
	if !c.Read {
		// git-lfs shows its progress only on a terminal unless told; the
		// app shows what a write prints as it comes.
		cmd.Env = append(cmd.Env, "GIT_LFS_FORCE_PROGRESS=1")
		if !c.NoPrompt {
			cmd.Env = append(cmd.Env, r.prompt...)
		}
	}
	if c.Untranslated {
		// LC_ALL would override LC_MESSAGES; the C locale ignores LANGUAGE.
		// LANG and LC_CTYPE stay, so paths and text decode as before.
		cmd.Env = append(cmd.Env, "LC_ALL=", "LC_MESSAGES=C")
	}
	cmd.Stdin = c.Stdin
	stopGroup(cmd, dies(c))
	return cmd
}

func (r *Runner) start(ctx context.Context, c *Cmd) (Result, error) {
	timeout := c.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	// The caller's ctx stays apart, so that its own deadline is not taken
	// for this one.
	caller := ctx
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if dies(c) {
		defer context.AfterFunc(r.quit, cancel)()
	}
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
	var stalled atomic.Bool
	if c.Stall > 0 {
		act := &activity{}
		act.touch()
		cmd.Stdout = io.MultiWriter(cmd.Stdout, act)
		cmd.Stderr = io.MultiWriter(cmd.Stderr, act)
		done := make(chan struct{})
		defer close(done)
		go r.watchStall(c.Stall, act, done, func() { stalled.Store(true); cancel() })
	}
	cmd.WaitDelay = 2 * time.Second
	var common string
	if stopsHard && !c.Read && c.GitDir != "" {
		common = commonDir(c.GitDir)
		r.writes.add(common, 1)
		defer r.writes.add(common, -1)
	}
	t0 := time.Now()
	r.running.Add(1)
	r.commands.Add(1)
	waited := watchGroup(cmd)
	err := r.Start(cmd)
	if err == nil {
		if c.Background {
			lower(cmd)
		}
		err = cmd.Wait()
		if errors.Is(err, exec.ErrWaitDelay) && ctx.Err() == nil {
			// git exited 0 (Wait says ErrWaitDelay only then); what still
			// held its output is something it started and left running, as
			// a hook's background job. What git printed has come.
			err = nil
		}
		if common != "" && err != nil && ctx.Err() != nil {
			removeLocks(c.GitDir, t0, r.writes.others(common))
		}
	}
	waited()
	r.running.Add(-1)
	r.commands.Add(-1)
	res := Result{Stdout: stdout.buf, Stderr: stderr.String(), Duration: time.Since(t0)}
	if err != nil {
		e := &Error{Line: c.Line(), ExitCode: -1, Stderr: res.Stderr, Err: err}
		var exit *exec.ExitError
		switch {
		case stdout.over:
			e.Err = fmt.Errorf("%w: over %d bytes", ErrOutputTooLarge, limit)
		case stalled.Load():
			e.Err = fmt.Errorf("%w: nothing heard for %v", ErrStalled, c.Stall)
		case errors.Is(caller.Err(), context.DeadlineExceeded):
			// The caller's own limit: how long git ran, not ours.
			e.Err = fmt.Errorf("%w after %v: %w", ErrTimeout, res.Duration.Round(time.Millisecond), caller.Err())
		case caller.Err() != nil:
			e.Err = caller.Err()
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			e.Err = fmt.Errorf("%w after %v", ErrTimeout, timeout)
		case ctx.Err() != nil:
			e.Err = ctx.Err()
		case errors.As(err, &exit):
			e.ExitCode = exit.ExitCode()
		}
		r.journal.record(c, e.Line, t0, res, e, false)
		return res, e
	}
	r.journal.record(c, c.Line(), t0, res, nil, false)
	return res, nil
}

// dies reports a command that should end with the app: a read, or one
// marked EndsWithApp where git removes its locks as it is stopped. A
// write the user started, and a tool the user opened, are let end.
func dies(c *Cmd) bool { return c.Read || c.EndsWithApp && !stopsHard }

// watchStall calls stop once act has seen nothing for stall, the time a
// question to the user is open aside, until done is closed.
func (r *Runner) watchStall(stall time.Duration, act *activity, done <-chan struct{}, stop func()) {
	tick := time.NewTicker(max(stall/8, 10*time.Millisecond))
	defer tick.Stop()
	for {
		select {
		case <-done:
			return
		case <-tick.C:
		}
		if r.asking != nil && r.asking() {
			act.touch()
			continue
		}
		if act.since() > stall {
			stop()
			return
		}
	}
}

// activity is when output was last written.
type activity struct{ last atomic.Int64 }

func (a *activity) touch() { a.last.Store(time.Now().UnixNano()) }

func (a *activity) since() time.Duration { return time.Duration(time.Now().UnixNano() - a.last.Load()) }

func (a *activity) Write(p []byte) (int, error) {
	a.touch()
	return len(p), nil
}

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
