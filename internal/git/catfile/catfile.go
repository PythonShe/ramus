// Package catfile keeps one long-lived git cat-file --batch-command process
// per repository and reads objects through it in batches.
package catfile

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"sync"

	"ramus/internal/git/run"
)

// MaxObject bounds the size of one object read through the process; a
// larger one is skipped (its bytes drained) and reported as too large.
const MaxObject = 64 << 20

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

// Process is a running cat-file. Its methods may be called from several
// goroutines; batches run one at a time.
type Process struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	in     *bufio.Writer
	stdin  io.Closer
	out    *bufio.Reader
	stderr bytes.Buffer
	dead   error
	cancel context.CancelFunc
	buf    []byte
	// untrack ends the runner's count of this process.
	untrack func()
}

// Start runs git cat-file --batch-command for the repository at gitDir.
// It runs as a read: it takes no optional locks and never fetches missing
// objects from a promisor remote, so it never writes and never touches the
// network. It stops when ctx is cancelled or Close is called.
func Start(ctx context.Context, r *run.Runner, gitDir string) (*Process, error) {
	ctx, cancel := context.WithCancel(ctx)
	cmd := r.Command(ctx, &run.Cmd{GitDir: gitDir, Read: true, Args: []string{"cat-file", "--batch-command", "--buffer"}})
	p := &Process{cmd: cmd, cancel: cancel}
	cmd.Stderr = &limitedWriter{w: &p.stderr, n: 64 << 10}
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
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	p.untrack = r.Track()
	p.stdin = stdin
	p.in = bufio.NewWriterSize(stdin, 64<<10)
	p.out = bufio.NewReaderSize(stdout, 256<<10)
	return p, nil
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
	var line [80]byte
	for _, id := range oids {
		b := append(line[:0], "contents "...)
		b = hex.AppendEncode(b, id)
		b = append(b, '\n')
		if _, err := p.in.Write(b); err != nil {
			return p.fail(err)
		}
	}
	if _, err := p.in.WriteString("flush\n"); err != nil {
		return p.fail(err)
	}
	if err := p.in.Flush(); err != nil {
		return p.fail(err)
	}
	var first error
	for i := range oids {
		o, err := p.next()
		if err != nil {
			return p.fail(err)
		}
		if first == nil {
			first = fn(i, o)
		}
	}
	return first
}

func (p *Process) next() (Object, error) {
	header, err := p.out.ReadSlice('\n')
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
		if _, err := p.out.Discard(int(size) + 1); err != nil {
			return Object{}, err
		}
		o.TooLarge = true
		return o, nil
	}
	if cap(p.buf) < int(size)+1 {
		p.buf = make([]byte, int(size)+1)
	}
	p.buf = p.buf[:size+1]
	if _, err := io.ReadFull(p.out, p.buf); err != nil {
		return Object{}, err
	}
	o.Data = p.buf[:size:size]
	return o, nil
}

func (p *Process) fail(err error) error {
	p.dead = fmt.Errorf("%w: %v: %s", ErrClosed, err, bytes.TrimSpace(p.stderr.Bytes()))
	p.cancel()
	return p.dead
}

// Close ends the process.
func (p *Process) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dead == nil {
		p.dead = ErrClosed
	}
	p.stdin.Close()
	err := p.cmd.Wait()
	p.cancel()
	p.untrack()
	if err != nil && !errors.Is(err, context.Canceled) {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil // killed or exited after stdin closed
		}
		return err
	}
	return nil
}

type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(b []byte) (int, error) {
	n := len(b)
	if len(b) > l.n {
		b = b[:l.n]
	}
	l.n -= len(b)
	l.w.Write(b)
	return n, nil
}
