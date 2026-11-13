// Package watch reports changes under a directory: a thin layer over each
// system's own notifications (FSEvents on macOS, ReadDirectoryChangesW on
// Windows, inotify on Linux). The notices are hints to look again, not a
// record of what happened: a batch names the paths that changed, or says
// that changes were dropped and everything must be looked at again.
package watch

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Batch is a set of changes, delivered together.
type Batch struct {
	// Paths changed, slash-separated and relative to the root; a directory
	// stands for anything under it.
	Paths []string
	// Overflow means notices were dropped, or there were too many to keep:
	// everything under the root may have changed.
	Overflow bool
}

// Options configure a watcher.
type Options struct {
	// Latency is how long a burst of changes collects before it is
	// delivered; DefaultLatency when zero.
	Latency time.Duration
	// SkipDir, when set, names directories (slash-separated, relative to
	// the root) not to watch where watches cost one per directory
	// (inotify), such as ignored build output. Changes in them may still
	// be reported where the system watches whole trees.
	SkipDir func(rel string) bool
}

// DefaultLatency collects a burst, such as a checkout writing thousands
// of files, into one batch.
const DefaultLatency = 50 * time.Millisecond

// maxPending bounds the paths kept between deliveries; past it a batch
// reports Overflow instead.
const maxPending = 1 << 14

// ErrLimit means the system's limit on watches was reached (inotify's
// max_user_watches). The app then refreshes when its window gains focus,
// and says so.
var ErrLimit = errors.New("watch: the system's limit on file watches is reached")

// ErrUnsupported means this system has no watcher here.
var ErrUnsupported = errors.New("watch: not supported on this system")

// Watcher watches one directory tree. Close it with the repository.
type Watcher struct {
	root    string
	latency time.Duration
	out     chan Batch
	notify  chan struct{}
	done    chan struct{}
	once    sync.Once
	stopped sync.WaitGroup
	plat    platform

	mu       sync.Mutex
	pending  map[string]struct{}
	overflow bool
	err      error
}

// platform is a system's watcher.
type platform interface {
	close() error
}

// New watches root and everything under it.
func New(root string, opt Options) (*Watcher, error) {
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	w := &Watcher{
		root: real, latency: opt.Latency,
		out: make(chan Batch, 1), notify: make(chan struct{}, 1), done: make(chan struct{}),
		pending: map[string]struct{}{},
	}
	if w.latency <= 0 {
		w.latency = DefaultLatency
	}
	if w.plat, err = start(w, opt); err != nil {
		return nil, err
	}
	w.stopped.Add(1)
	go w.deliver()
	return w, nil
}

// Root returns the watched directory, symlinks resolved.
func (w *Watcher) Root() string { return w.root }

// Events delivers batches until Close.
func (w *Watcher) Events() <-chan Batch { return w.out }

// Err returns why the watcher stopped on its own, such as ErrLimit.
func (w *Watcher) Err() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

// Close stops the watcher and closes Events.
func (w *Watcher) Close() error {
	var err error
	w.once.Do(func() {
		err = w.plat.close()
		close(w.done)
		w.stopped.Wait()
		close(w.out)
	})
	return err
}

// changed records a path the system reported, absolute or relative to
// the root, from any thread.
func (w *Watcher) changed(path string) {
	rel := path
	if filepath.IsAbs(path) {
		r, err := filepath.Rel(w.root, path)
		if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			return
		}
		rel = r
	}
	rel = filepath.ToSlash(rel)
	w.mu.Lock()
	if !w.overflow {
		if len(w.pending) >= maxPending {
			w.overflow, w.pending = true, map[string]struct{}{}
		} else {
			w.pending[rel] = struct{}{}
		}
	}
	w.mu.Unlock()
	w.poke()
}

// dropped records that the system lost notices.
func (w *Watcher) dropped() {
	w.mu.Lock()
	w.overflow, w.pending = true, map[string]struct{}{}
	w.mu.Unlock()
	w.poke()
}

// fail records why the system watcher stopped; the app falls back to
// refreshing on focus.
func (w *Watcher) fail(err error) {
	w.mu.Lock()
	if w.err == nil {
		w.err = err
	}
	w.mu.Unlock()
	w.dropped()
}

func (w *Watcher) poke() {
	select {
	case w.notify <- struct{}{}:
	default:
	}
}

func (w *Watcher) deliver() {
	defer w.stopped.Done()
	for {
		select {
		case <-w.done:
			return
		case <-w.notify:
		}
		// Let the burst finish.
		select {
		case <-w.done:
			return
		case <-time.After(w.latency):
		}
		w.mu.Lock()
		b := Batch{Overflow: w.overflow}
		if !b.Overflow {
			b.Paths = make([]string, 0, len(w.pending))
			for p := range w.pending {
				b.Paths = append(b.Paths, p)
			}
		}
		w.pending, w.overflow = map[string]struct{}{}, false
		w.mu.Unlock()
		if !b.Overflow && len(b.Paths) == 0 {
			continue
		}
		select {
		case w.out <- b:
		case <-w.done:
			return
		}
	}
}
