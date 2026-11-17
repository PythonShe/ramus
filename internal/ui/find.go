package ui

import "github.com/egoist/mygo/ui"

// findFields are the search and filter fields Find focuses, one of each
// view that has one; a view binds its own as it builds it.
type findFields struct {
	welcome, changes, refs ui.Handle
}

// focusFind focuses the search or filter field of what the window
// shows, with its text selected for typing anew: Find.
func (a *App) focusFind() {
	t := a.current()
	switch {
	case t == nil:
		a.find.welcome.SelectAll()
	case t.sel == "changes":
		a.find.changes.SelectAll()
	case t.sel == "history":
		a.focusHistoryFind(t)
	case t.sel == "refs":
		a.find.refs.SelectAll()
	}
}
