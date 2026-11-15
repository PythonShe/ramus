package ui

import (
	"github.com/egoist/mygo/ui"

	"ramus/internal/git/diff"
	"ramus/locales"
)

// lineCounts shows the lines added and deleted, as a diff stat does,
// leaving out a count of none.
func lineCounts(c *ui.Context, added, deleted int) {
	inkLineCounts(c, plainInk(c.Theme()), added, deleted)
}

// inkLineCounts is lineCounts in k, as on a row chosen.
func inkLineCounts(c *ui.Context, k ink, added, deleted int) {
	if added <= 0 && deleted <= 0 {
		return
	}
	ui.Row(c).Gap(spaceXS).AlignItems(ui.Center).Description(locales.LinesChanged(added, deleted)).Children(func() {
		if added > 0 {
			ui.Text(c, locales.LinesAdded(added)).FontSize(textSmall).TextColor(k.Success).SingleLine()
		}
		if deleted > 0 {
			ui.Text(c, locales.LinesDeleted(deleted)).FontSize(textSmall).TextColor(k.Danger).SingleLine()
		}
	})
}

// fileLines shows what git counted of a file's change: its lines, or
// that it is binary; nothing while it is not counted.
func fileLines(c *ui.Context, l diff.Lines) {
	switch {
	case !l.Known:
	case l.Binary:
		ui.Text(c, locales.PatchBinary()).FontSize(textSmall).TextColor(c.Theme().TextMuted).SingleLine()
	default:
		lineCounts(c, int(l.Added), int(l.Deleted))
	}
}

// fileLabel is what a changed file is read aloud as: its state and path,
// and the lines its change adds and deletes once counted.
func fileLabel(state, path string, l diff.Lines) string {
	if !l.Known || l.Binary || l.Added == 0 && l.Deleted == 0 {
		return locales.ChangesFileLabel(state, path)
	}
	return locales.ChangesFileLinesLabel(int(l.Added), int(l.Deleted), state, path)
}

// lineAt is file i's count in ls, the zero Lines while not counted.
func lineAt(ls []diff.Lines, i int) diff.Lines {
	if i < 0 || i >= len(ls) {
		return diff.Lines{}
	}
	return ls[i]
}
