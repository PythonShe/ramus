package session

import (
	"fmt"
	"runtime/debug"

	"ramus/internal/git/lifetime"
	"ramus/internal/git/redact"
)

// OnPanic, when set, hears of every panic recovered in a repository's
// work, on whichever goroutine it happened, for the crash log.
var OnPanic func(path string, p lifetime.Panic)

// panicked reports a panic in the repository's work, off the main
// thread: the tab shows the first.
func (s *Session) panicked(p lifetime.Panic) {
	p.Value = redact.Shown(p.Value)
	if OnPanic != nil {
		OnPanic(s.Path, p)
	}
	s.update(func() {
		if s.Panic == nil {
			s.Panic = &p
		}
	})
}

// recoverWork, deferred in a goroutine of the session that no lifetime
// started, reports a panic in it.
func (s *Session) recoverWork() {
	if v := recover(); v != nil {
		s.panicked(lifetime.Panic{Value: fmt.Sprint(v), Stack: string(debug.Stack())})
	}
}

// recoverMain, deferred around what work hands the main thread, reports
// a panic in it there.
func (s *Session) recoverMain() {
	if v := recover(); v != nil {
		p := lifetime.Panic{Value: redact.Shown(fmt.Sprint(v)), Stack: string(debug.Stack())}
		if OnPanic != nil {
			OnPanic(s.Path, p)
		}
		if s.Panic == nil {
			s.Panic = &p
		}
	}
}
