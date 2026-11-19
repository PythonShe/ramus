package ui

import "github.com/egoist/mygo/ui"

// keepTop sets how keyed list l keeps its place as rows come above the
// row at its top. With a Key alone, mygo keeps that row at the top even
// while the list shows its top, which hides what came above it: a new
// reflog entry, stash or commit. So a list showing its top keeps its
// offset and shows them; one the user scrolled keeps their row at its
// top. Call it before building l, each frame.
func keepTop(l *ui.ListState) {
	first, _ := l.Visible()
	l.KeepOffset = first <= 0
}
