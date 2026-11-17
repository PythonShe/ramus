package ui

import "github.com/egoist/mygo/ui"

// findFields are the search and filter fields Find focuses, one of each
// view that has one; a view binds its own as it builds it.
type findFields struct {
	welcome, changes, refs ui.Handle
	// frames counts down the frames a Find waits for its field to show.
	frames int
}

// focusFind focuses the search or filter field of what the window
// shows, with its text selected for typing anew: Find.
func (a *App) focusFind() {
	// A Find still waiting for another view's field would take the focus
	// there once that field shows.
	a.find.cancel()
	t := a.current()
	if t != nil {
		t.hist.focusFind = false
	}
	var h *ui.Handle
	switch {
	case t == nil:
		h = &a.find.welcome
	case t.sel == "changes":
		h = &a.find.changes
	case t.sel == "history":
		a.focusHistoryFind(t)
	case t.sel == "refs":
		h = &a.find.refs
	}
	if h != nil {
		h.SelectAll()
		// Asked from a shortcut, the field takes the focus in this frame;
		// from the menu, between frames, in the next.
		a.find.frames = 2
	}
}

// expire drops a Find whose field did not show in the frames after it,
// as Remotes, which has no filter: it would otherwise wait for the field
// and take the focus once it shows, the user typing elsewhere by then.
func (f *findFields) expire() {
	if f.frames == 0 {
		return
	}
	if f.frames--; f.frames == 0 {
		f.cancel()
	}
}

// cancel drops the Find waiting for a field, if one is.
func (f *findFields) cancel() {
	f.frames = 0
	f.welcome.CancelFocus()
	f.changes.CancelFocus()
	f.refs.CancelFocus()
}
