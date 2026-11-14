package ui

import (
	"fmt"
	"runtime/debug"

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

// goWrite is goWork for a write of a file, which a test waits for
// (writes) before the file's folder goes.
func (a *App) goWrite(fn func()) {
	a.writes.Add(1)
	a.goWork(nil, func() {
		defer a.writes.Done()
		fn()
	})
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
