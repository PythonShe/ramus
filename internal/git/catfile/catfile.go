// Package catfile keeps one git cat-file --batch-command process per
// repository and reads objects through it in batches.
package catfile

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"ramus/internal/git/run"
)

// MaxObject bounds the size of one object read through the process; a
// larger one is skipped (its bytes drained) and reported as too large.
const MaxObject = 64 << 20

// IdleTimeout is how long git runs with nothing to read before it is
// ended; the next Read starts it again. A running cat-file keeps the
// repository's packs open, which on Windows stops git gc from deleting
// them, and frees their space late everywhere.
const IdleTimeout = 10 * time.Second

// StallTimeout is how long a batch waits for git to take a request or
// give an answer before git is taken to have stopped, as on a stalled
// network file system, and ended.
const StallTimeout = time.Minute

// keepBuf is the largest object buffer kept from one batch to the next:
// a large blob read once is not held for the repository's life.
const keepBuf = 1 << 20

// ErrClosed is returned after Close, or once the process has died.
var ErrClosed = errors.New("catfile: process closed")

// Object is one answer of a batch.
type Object struct {
	Type    string // "commit", "tree", "blob", "tag"; empty when missing
	Size    int64
	Data    []byte // valid only during the callback
	Missing bool
	// TooLarge is set, with Data nil, for an object over MaxObject.
	TooLarge bool
}

// Process reads objects through a cat-file it starts when there is
// something to read. Its methods may be called from several goroutines;
// batches run one at a time.
type Process struct {
	ctx    context.Context
	runner *run.Runner
	gitDir string
	// batch is held by the batch running; a channel rather than a mutex,
	// so that a read waiting for its turn can be cancelled.
	batch chan struct{}
	// buf is the batch's; one past keepBuf is not kept past its batch.
	buf []byte

	// mu guards what follows; it is never held while git is waited on.
	mu sync.Mutex
	// g is the running git, nil while idle.
	g         *proc
	dead      error
	idle      time.Duration
	stall     time.Duration
	idleTimer *time.Timer
	used      time.Time
	// ending counts gits let go of that have not exited yet.
	ending sync.WaitGroup
}

// proc is one cat-file process.
type proc struct {
	cmd    *exec.Cmd
	in     *bufio.Writer
	stdin  io.Closer
	out    *bufio.Reader
	stdout io.Closer
	stderr *limitedWriter
	cancel context.CancelFunc
	// untrack ends the runner's count of this process.
	untrack func()
	ended   sync.Once
	err     error
	// stuck is a git the watchdog stopped: one hung on a stalled volume
	// may never die, so no one waits for it.
	stuck atomic.Bool
}

// letGo ends g, waiting for it unless it is stuck, which is ended in the
// background, out of its caller's way.
func (g *proc) letGo() error {
	if g.stuck.Load() {
		go g.end()
		return nil
	}
	return g.end()
}

// Start runs git cat-file --batch-command for the repository at gitDir.
// It runs as a read: it takes no optional locks and never fetches missing
// objects from a promisor remote, so it never writes and never touches the
// network. It stops when ctx is cancelled or Close is called.
func Start(ctx context.Context, r *run.Runner, gitDir string) (*Process, error) {
	if abs, err := filepath.Abs(gitDir); err == nil {
		gitDir = abs
	}
	p := &Process{ctx: ctx, runner: r, gitDir: gitDir, batch: make(chan struct{}, 1), idle: IdleTimeout, stall: StallTimeout}
	g, err := p.start(false)
	if err != nil {
		return nil, err
	}
	p.g = g
	return p, nil
}

// start starts git; again is a start after the first, which the command
// log has already.
func (p *Process) start(again bool) (*proc, error) {
	ctx, cancel := context.WithCancel(p.ctx)
	prepare := p.runner.Command
	if again {
		prepare = p.runner.Again
	}
	cmd := prepare(ctx, &run.Cmd{GitDir: p.gitDir, Read: true, Args: []string{"cat-file", "--batch-command", "--buffer"}})
	// It names the repository by --git-dir; a working directory inside it
	// would stop Windows from moving or deleting the folder.
	cmd.Dir = os.TempDir()
	g := &proc{cmd: cmd, cancel: cancel, stderr: &limitedWriter{n: 64 << 10}}
	cmd.Stderr = g.stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := p.runner.Start(cmd); err != nil {
		cancel()
		return nil, err
	}
	g.untrack = p.runner.Track()
	g.stdin, g.stdout = stdin, stdout
	g.in = bufio.NewWriterSize(stdin, 64<<10)
	g.out = bufio.NewReaderSize(stdout, 256<<10)
	return g, nil
}

// end stops the process and waits for it.
func (g *proc) end() error {
	g.ended.Do(func() {
		g.stdin.Close()
		g.cancel()
		g.err = g.cmd.Wait()
		g.untrack()
	})
	return g.err
}

// Read is ReadContext without a context.
func (p *Process) Read(oids [][]byte, fn func(i int, o Object) error) error {
	return p.ReadContext(context.Background(), oids, fn)
}

// ReadContext asks for every object in oids, then calls fn with each
// answer in the same order; an id that is not a whole object id is
// answered missing. If fn returns an error the rest of the batch
// is drained and that error returned. Cancelling ctx, while the read
// waits for its turn or reads, returns ctx's error.
func (p *Process) ReadContext(ctx context.Context, oids [][]byte, fn func(i int, o Object) error) (err error) {
	select {
	case p.batch <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-p.batch }()
	if err := ctx.Err(); err != nil {
		return err
	}
	g, err := p.running()
	if err != nil {
		return err
	}
	defer p.rest()
	// A batch is cut short only by ending git: its answers cannot be
	// skipped, and git, blocked writing them, would never take another.
	stopCancel := context.AfterFunc(ctx, g.cancel)
	w := p.watch(g)
	var broken error
	read := false
	defer func() {
		if cap(p.buf) > keepBuf {
			p.buf = nil
		}
		cancelled := !stopCancel()
		stalled := w.stop()
		switch {
		case cancelled:
			p.drop(g)
			err = ctx.Err()
		case read && stalled:
			p.drop(g)
		case read:
		case stalled:
			err = p.fail(g, fmt.Errorf("%w: cat-file answered nothing for %v", run.ErrStalled, p.stall))
		case broken != nil:
			err = p.fail(g, broken)
		default:
			// fn panicked, leaving the rest of the batch unread.
			p.fail(g, errors.New("catfile: batch abandoned"))
		}
	}()
	var line [80]byte
	for _, id := range oids {
		if !whole(id) {
			continue
		}
		b := append(line[:0], "contents "...)
		b = hex.AppendEncode(b, id)
		b = append(b, '\n')
		if _, broken = g.in.Write(b); broken != nil {
			return
		}
		w.seen.Add(1)
	}
	if _, broken = g.in.WriteString("flush\n"); broken != nil {
		return
	}
	if broken = g.in.Flush(); broken != nil {
		return
	}
	var first error
	for i, id := range oids {
		o := Object{Missing: true}
		if whole(id) {
			if o, broken = p.next(g); broken != nil {
				return
			}
		}
		w.seen.Add(1)
		if first == nil {
			w.inFn.Store(true)
			first = fn(i, o)
			w.inFn.Store(false)
		}
	}
	read = true
	return first
}

// whole reports whether id is a whole object id, SHA-1 or SHA-256. git
// would take a shorter one for an abbreviation, which can name another
// object, and answers an empty one in a form the batch cannot read.
func whole(id []byte) bool { return len(id) == 20 || len(id) == 32 }

// running returns the running git, starting one if there is none.
func (p *Process) running() (*proc, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dead != nil {
		return nil, p.dead
	}
	if p.g == nil {
		g, err := p.start(true)
		if err != nil {
			return nil, err
		}
		p.g = g
	}
	return p.g, nil
}

// drop lets go of a git ended under a batch; the next read starts
// another.
func (p *Process) drop(g *proc) {
	g.cancel()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dead != nil || p.g != g {
		return // Close ends it
	}
	p.g = nil
	if g.stuck.Load() {
		g.letGo() // Close need not wait for it
		return
	}
	p.ending.Add(1)
	go func() {
		defer p.ending.Done()
		g.end()
	}()
}

// fail ends the process for good: CatFile starts another.
func (p *Process) fail(g *proc, err error) error {
	g.cancel()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dead == nil {
		p.dead = fmt.Errorf("%w: %v: %s", ErrClosed, err, bytes.TrimSpace(g.stderr.Bytes()))
	}
	return p.dead
}

// watchdog ends git when a batch has seen no progress for a while; time
// spent in the batch's callback is not git's.
type watchdog struct {
	seen    atomic.Uint64
	inFn    atomic.Bool
	mu      sync.Mutex
	done    bool
	stalled bool
	t       *time.Timer
}

func (p *Process) watch(g *proc) *watchdog {
	w := &watchdog{}
	last := uint64(0)
	var fire func()
	fire = func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.done {
			return
		}
		if n := w.seen.Load(); n != last || w.inFn.Load() {
			last = n
			w.t.Reset(p.stall)
			return
		}
		w.stalled = true
		g.stuck.Store(true)
		g.cancel()
		// A git that cannot die, hung on a stalled volume, would hold
		// the batch on its pipes for good: closed, they give it up. In a
		// goroutine, as a close may wait on a read where pipes are not
		// polled (Windows).
		go func() {
			g.stdout.Close()
			g.stdin.Close()
		}()
	}
	w.mu.Lock()
	w.t = time.AfterFunc(p.stall, fire)
	w.mu.Unlock()
	return w
}

// stop ends the watch and reports whether it ended git.
func (w *watchdog) stop() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.done = true
	w.t.Stop()
	return w.stalled
}

// rest arms the end of an idle git, once a batch is over.
func (p *Process) rest() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dead != nil || p.g == nil {
		return
	}
	p.used = time.Now()
	if p.idleTimer == nil {
		p.idleTimer = time.AfterFunc(p.idle, p.endIdle)
	} else {
		p.idleTimer.Reset(p.idle)
	}
}

func (p *Process) endIdle() {
	select {
	case p.batch <- struct{}{}:
	default:
		return // the batch running arms the timer again when it is over
	}
	p.mu.Lock()
	g := p.g
	if p.dead != nil || g == nil || time.Since(p.used) < p.idle {
		p.mu.Unlock()
		<-p.batch
		return
	}
	p.g = nil
	p.ending.Add(1)
	p.mu.Unlock()
	<-p.batch
	defer p.ending.Done()
	g.end()
}

func (p *Process) next(g *proc) (Object, error) {
	header, err := g.out.ReadSlice('\n')
	if err != nil {
		return Object{}, err
	}
	header = bytes.TrimSuffix(header, []byte{'\n'})
	f := bytes.Fields(header)
	if len(f) == 2 && string(f[1]) == "missing" {
		return Object{Missing: true}, nil
	}
	if len(f) != 3 {
		return Object{}, fmt.Errorf("catfile: unexpected header %q", header)
	}
	typ := string(f[1])
	size, err := strconv.ParseInt(string(f[2]), 10, 64)
	if err != nil || size < 0 {
		return Object{}, fmt.Errorf("catfile: bad size in %q", header)
	}
	o := Object{Type: typ, Size: size}
	if size > MaxObject {
		if _, err := io.CopyN(io.Discard, g.out, size+1); err != nil {
			return Object{}, err
		}
		o.TooLarge = true
		return o, nil
	}
	if cap(p.buf) < int(size)+1 {
		p.buf = make([]byte, int(size)+1)
	}
	p.buf = p.buf[:size+1]
	if _, err := io.ReadFull(g.out, p.buf); err != nil {
		return Object{}, err
	}
	if p.buf[size] != '\n' {
		return Object{}, fmt.Errorf("catfile: object %s not followed by a newline", f[0])
	}
	o.Data = p.buf[:size:size]
	return o, nil
}

// Alive reports whether the process can still read. It does not wait
// for a batch another goroutine is reading.
func (p *Process) Alive() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dead == nil
}

// closeWait bounds how long Close waits for git to end: far past what a
// git killed takes.
const closeWait = 10 * time.Second

// Close ends the process. A batch still reading fails at once.
func (p *Process) Close() error {
	p.mu.Lock()
	if p.dead == nil {
		p.dead = ErrClosed
	}
	if p.idleTimer != nil {
		p.idleTimer.Stop()
	}
	g := p.g
	p.mu.Unlock()
	// Bounded: a git that cannot die, hung on a stalled volume, must not
	// hold its caller, which may hold the repository.
	ended := make(chan error, 1)
	go func() {
		p.ending.Wait()
		if g == nil {
			ended <- nil
			return
		}
		ended <- g.letGo()
	}()
	var err error
	select {
	case err = <-ended:
	case <-time.After(closeWait):
		if g != nil {
			go func() { g.stdout.Close(); g.stdin.Close() }()
		}
		return fmt.Errorf("catfile: git did not end within %v", closeWait)
	}
	// exec says ErrWaitDelay only when git exited 0 and something it
	// left running still held its output: not a failure of git's.
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, exec.ErrWaitDelay) {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil // killed or exited after stdin closed
		}
		return err
	}
	return nil
}

// limitedWriter keeps the first n bytes written to it. exec copies the
// process's stderr into it from a goroutine of its own while fail may
// read it, so it is locked.
type limitedWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
	n   int
}

func (l *limitedWriter) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(b)
	if len(b) > l.n {
		b = b[:l.n]
	}
	l.n -= len(b)
	l.buf.Write(b)
	return n, nil
}

// Bytes returns a copy of what was kept.
func (l *limitedWriter) Bytes() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return bytes.Clone(l.buf.Bytes())
}
