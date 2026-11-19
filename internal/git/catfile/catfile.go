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
	mu     sync.Mutex
	ctx    context.Context
	runner *run.Runner
	gitDir string
	// g is the running git, nil while idle.
	g    *proc
	dead error
	buf  []byte
	idle      time.Duration
	idleTimer *time.Timer
	used      time.Time
	// ending counts gits ended for idleness that have not exited yet.
	ending sync.WaitGroup
}

// proc is one cat-file process.
type proc struct {
	cmd    *exec.Cmd
	in     *bufio.Writer
	stdin  io.Closer
	out    *bufio.Reader
	stderr *limitedWriter
	cancel context.CancelFunc
	// untrack ends the runner's count of this process.
	untrack func()
	ended   sync.Once
	err     error
}

// Start runs git cat-file --batch-command for the repository at gitDir.
// It runs as a read: it takes no optional locks and never fetches missing
// objects from a promisor remote, so it never writes and never touches the
// network. It stops when ctx is cancelled or Close is called.
func Start(ctx context.Context, r *run.Runner, gitDir string) (*Process, error) {
	if abs, err := filepath.Abs(gitDir); err == nil {
		gitDir = abs
	}
	p := &Process{ctx: ctx, runner: r, gitDir: gitDir, idle: IdleTimeout}
	g, err := p.start()
	if err != nil {
		return nil, err
	}
	p.g = g
	return p, nil
}

func (p *Process) start() (*proc, error) {
	ctx, cancel := context.WithCancel(p.ctx)
	cmd := p.runner.Command(ctx, &run.Cmd{GitDir: p.gitDir, Read: true, Args: []string{"cat-file", "--batch-command", "--buffer"}})
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
	g.stdin = stdin
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

// Read asks for every object in oids, then calls fn with each answer in the
// same order. If fn returns an error the rest of the batch is drained and
// that error returned.
func (p *Process) Read(oids [][]byte, fn func(i int, o Object) error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dead != nil {
		return p.dead
	}
	if p.g == nil {
		g, err := p.start()
		if err != nil {
			return err
		}
		p.g = g
	}
	defer p.rest()
	g := p.g
	var line [80]byte
	for _, id := range oids {
		b := append(line[:0], "contents "...)
		b = hex.AppendEncode(b, id)
		b = append(b, '\n')
		if _, err := g.in.Write(b); err != nil {
			return p.fail(err)
		}
	}
	if _, err := g.in.WriteString("flush\n"); err != nil {
		return p.fail(err)
	}
	if err := g.in.Flush(); err != nil {
		return p.fail(err)
	}
	// A panic in fn leaves the rest of the batch unread: the next batch
	// would read its answers, and git, blocked writing them, would never
	// end. The process is ended instead, for another to take its place.
	read := false
	defer func() {
		if !read && p.dead == nil {
			p.fail(errors.New("catfile: batch abandoned"))
		}
	}()
	var first error
	for i := range oids {
		o, err := p.next(g)
		if err != nil {
			return p.fail(err)
		}
		if first == nil {
			first = fn(i, o)
		}
	}
	read = true
	return first
}

// rest arms the end of an idle git, once a batch is over.
func (p *Process) rest() {
	if p.dead != nil {
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
	// A batch running now arms the timer again when it is over.
	if !p.mu.TryLock() {
		return
	}
	g := p.g
	if p.dead != nil || g == nil || time.Since(p.used) < p.idle {
		p.mu.Unlock()
		return
	}
	p.g = nil
	p.ending.Add(1)
	p.mu.Unlock()
	g.end()
	p.ending.Done()
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

func (p *Process) fail(err error) error {
	p.dead = fmt.Errorf("%w: %v: %s", ErrClosed, err, bytes.TrimSpace(p.g.stderr.Bytes()))
	p.g.cancel()
	return p.dead
}

// Alive reports whether the process can still read; it waits for a batch
// another goroutine is reading.
func (p *Process) Alive() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dead == nil
}

// Close ends the process.
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
	p.ending.Wait()
	if g == nil {
		return nil
	}
	err := g.end()
	if err != nil && !errors.Is(err, context.Canceled) {
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
