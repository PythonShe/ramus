package ui

import (
	"fmt"
	"runtime/debug"
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

// WaitWrites waits, up to d, for the files being written off the main
// thread (the library's record of a repository opened, forgotten or
// grouped) to be written, at a quit, after which the process may end at
// once. It reports whether they were.
func (a *App) WaitWrites(d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		a.writes.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

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
