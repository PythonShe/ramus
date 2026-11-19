package ui

import (
	"errors"

	"ramus/internal/session"
)

// reopened reports an answer of a read whose repository closed and opened
// again under it, as Trust does: nothing went wrong, and the view asks
// again of the repository open now rather than show it.
func reopened(err error) bool { return errors.Is(err, session.ErrReopened) }

// askAgain has ask run once tab t's repository is open again, for a read
// the reopen answered that no frame of its view asks again by itself, as
// one a click started. ask checks that what it asks for is still wanted.
// A read asked again of the repository open now is not answered so
// unless it opens again too, so nothing loops.
func (a *App) askAgain(t *tab, ask func()) { t.again = append(t.again, ask) }

// runAgain runs what askAgain kept, on a frame of tab t once its
// repository is open.
func (a *App) runAgain(t *tab) {
	if len(t.again) == 0 || t.s.Repo == nil {
		return
	}
	asks := t.again
	t.again = nil
	for _, ask := range asks {
		ask()
	}
}
