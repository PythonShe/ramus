// Package lifetime ties everything a repository owns to one span that
// ends when the repository closes: goroutines, subprocesses (through the
// context), memory mappings and anything else that must be released. The
// app opens and closes repositories for as long as it runs, so nothing
// may outlive its repository.
package lifetime

import (
	"context"
	"errors"
	"io"
	"sync"

	"ramus/internal/git/mmap"
)

// ErrClosed is returned for work started after Close.
var ErrClosed = errors.New("lifetime: repository closed")

// Lifetime is safe for use by several goroutines.
type Lifetime struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	done   chan struct{}

	mu      sync.Mutex
	closed  bool
	closers map[*owned]struct{}
	err     error
}

type owned struct{ c io.Closer }

// New starts a lifetime under parent.
func New(parent context.Context) *Lifetime {
	ctx, cancel := context.WithCancel(parent)
	return &Lifetime{ctx: ctx, cancel: cancel, done: make(chan struct{}), closers: map[*owned]struct{}{}}
}

// Context is cancelled when the lifetime ends; every subprocess and
// goroutine of the repository runs under it.
func (l *Lifetime) Context() context.Context { return l.ctx }

// Go runs fn in a goroutine that Close waits for. fn must return soon
// after ctx is cancelled.
func (l *Lifetime) Go(fn func(ctx context.Context)) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return ErrClosed
	}
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		fn(l.ctx)
	}()
	return nil
}

// Own closes c when the lifetime ends, after every goroutine has
// returned. The returned release closes c early and forgets it; it is safe
// to call more than once.
func (l *Lifetime) Own(c io.Closer) (release func() error, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		c.Close()
		return nil, ErrClosed
	}
	o := &owned{c}
	l.closers[o] = struct{}{}
	return func() error {
		l.mu.Lock()
		_, ok := l.closers[o]
		delete(l.closers, o)
		l.mu.Unlock()
		if !ok {
			return nil
		}
		return c.Close()
	}, nil
}

// Map memory-maps a file for the rest of the lifetime: it is unmapped at
// Close, once no goroutine of the repository can be reading it. The UI
// must drop what it holds from the mapping before closing the repository.
func (l *Lifetime) Map(path string) (*mmap.File, error) {
	f, err := mmap.Open(path)
	if err != nil {
		return nil, err
	}
	if _, err := l.Own(f); err != nil {
		return nil, err
	}
	return f, nil
}

// Close ends the lifetime without waiting: it cancels the context, which
// stops subprocesses, and in the background waits for the goroutines and
// then releases what it owns. Done is closed when all of it is over.
func (l *Lifetime) Close() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	l.mu.Unlock()
	l.cancel()
	go func() {
		l.wg.Wait()
		l.mu.Lock()
		cs := l.closers
		l.closers = nil
		l.mu.Unlock()
		var first error
		for o := range cs {
			if err := o.c.Close(); err != nil && first == nil {
				first = err
			}
		}
		l.mu.Lock()
		l.err = first
		l.mu.Unlock()
		close(l.done)
	}()
}

// Done is closed once Close has finished releasing everything.
func (l *Lifetime) Done() <-chan struct{} { return l.done }

// Err returns the first error from releasing, after Done.
func (l *Lifetime) Err() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}
