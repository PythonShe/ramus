package ui

import (
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"ramus/internal/crashlog"
	"ramus/internal/git/lifetime"
	"ramus/internal/git/redact"
	"ramus/locales"
)

// goWork runs fn in a goroutine of its own. A panic in it does not end
// the app: with t, it is one in the work of t's repository, which its tab
// shows and offers to reopen; without, it goes to the crash log and is
// said in a notice.
func (a *App) goWork(t *tab, fn func()) {
	if t != nil {
		s := t.s
		go func() {
			defer s.Recover()
			fn()
		}()
		return
	}
	update := a.Update
	goRecover(a.Crash, fn, func(p lifetime.Panic) {
		update(func() { a.notice = locales.AppPanic(p.Value) })
	})
}

// goWrite is goWork for a write of a file, which a quit waits for
// (writes), as a test does before the file's folder goes. fn writes and
// returns what to do after, if anything: showing the result, which
// waits for the main thread. That runs once the write counts as done, so
// that a quit, which holds the main thread while it waits, never waits
// for it.
func (a *App) goWrite(fn func() (then func())) {
	a.writes.Add(1)
	a.goWork(nil, func() {
		var then func()
		func() {
			defer a.writes.Done()
			then = fn()
		}()
		if then != nil {
			then()
		}
	})
}

// lastWrites has the writes of one thing, by key, land in the order
// they were asked: each is numbered as it is asked, on the main thread,
// and they run one at a time, a write with a later one asked behind it
// dropped, as the later writes all it would. Without it, two writes on
// goroutines of their own land in either order, and the older may be
// the one left.
type lastWrites struct {
	// mu guards n and asked, the number of the last write asked of each
	// key that has one not done. The main thread takes it, so it is
	// never held across a write.
	mu    sync.Mutex
	n     uint64
	asked map[string]uint64
	// write is held across a write, never on the main thread.
	write sync.Mutex
}

// ask numbers a write of key, to hand to do.
func (lw *lastWrites) ask(key string) uint64 {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	if lw.asked == nil {
		lw.asked = map[string]uint64{}
	}
	lw.n++
	lw.asked[key] = lw.n
	return lw.n
}

// do runs write, the write of key numbered n, after the writes running,
// unless a later one of key was asked. It reports whether write ran.
func (lw *lastWrites) do(key string, n uint64, write func()) bool {
	lw.write.Lock()
	defer lw.write.Unlock()
	lw.mu.Lock()
	last := lw.asked[key] == n
	lw.mu.Unlock()
	if !last {
		return false
	}
	defer func() {
		lw.mu.Lock()
		if lw.asked[key] == n {
			delete(lw.asked, key)
		}
		lw.mu.Unlock()
	}()
	write()
	return true
}

// WaitWrites waits, up to d, for the files being written off the main
// thread (the library's record of a repository opened, forgotten or
// grouped) to be written, at a quit, after which the process may end at
// once. It reports whether they were.
func (a *App) WaitWrites(d time.Duration) bool { return waitFor(a.writes.idle(), d) }

// waitFor waits, up to d, for done to close, and reports whether it did.
// It leaves nothing behind once it returns.
func waitFor(done <-chan struct{}, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	}
}

// writeGroup counts the writes running, as a sync.WaitGroup does, with a
// channel closed once none is, so that a wait for them can be bounded
// without a goroutine of its own left waiting past the bound.
type writeGroup struct {
	mu sync.Mutex
	n  int
	// done is closed while n is 0; nil before the first write.
	done chan struct{}
}

// Add adds d to the writes running, which must not go below 0.
func (g *writeGroup) Add(d int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.n == 0 && d > 0 {
		g.done = make(chan struct{})
	}
	g.n += d
	switch {
	case g.n < 0:
		panic("ui: negative writeGroup count")
	case g.n == 0 && d < 0:
		close(g.done)
	}
}

// Done ends one write.
func (g *writeGroup) Done() { g.Add(-1) }

// idle is closed once no write runs.
func (g *writeGroup) idle() <-chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.n == 0 {
		return closedChan
	}
	return g.done
}

// Wait waits until no write runs.
func (g *writeGroup) Wait() { <-g.idle() }

var closedChan = func() chan struct{} { c := make(chan struct{}); close(c); return c }()

// goRecover runs fn in a goroutine of its own and hands a panic in it to
// the crash log, when there is one, and to shown, instead of ending the
// app.
func goRecover(crash *crashlog.Log, fn func(), shown func(lifetime.Panic)) {
	go func() {
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			p := lifetime.Panic{Value: redact.Shown(fmt.Sprint(v)), Stack: string(debug.Stack())}
			if crash != nil {
				crash.Panic("", "", p) // a report not kept is only not kept
			}
			shown(p)
		}()
		fn()
	}()
}
