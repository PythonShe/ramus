package ui

import (
	"github.com/egoist/mygo/ui"

	"ramus/internal/git/diff"
	"ramus/locales"
)

// folderPane fills the patch's pane for a folder git does not track,
// whose files the list shows under it: what is in it, and what can be
// done with it whole. A repository inside the work tree, which git lists
// as such a folder, holds nothing listed.
func (a *App) folderPane(c *ui.Context, t *tab, m *changeModel, r changeRow) {
	th := c.Theme()
	ro := t.s.Queue == nil
	shown, _ := showPath(m.path(r))
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(barPadY, insetX).MinHeight(barHeight).Gap(spaceM).AlignItems(ui.Center).Children(func() {
			pathLabel(c, shown).Bold().Grow(1)
			if f, ok := m.listed(r.idx); ok && m.isFolder(r) {
				ui.Text(c, locales.ChangesFolderFiles(len(f.Paths)+f.More)).FontSize(textSmall).TextColor(th.TextMuted)
				fileLines(c, t.cv.rowLines(m, r))
			}
			if ui.Button(c, locales.ChangesDiscardFolder()).Disabled(ro).Clicked() {
				a.discardRows(t, m, r.sec, []changeRow{r})
			}
			if ui.Button(c, locales.ChangesStageFolder()).Disabled(ro).Clicked() {
				a.toggleStage(t, m, []changeRow{r})
			}
		})
		emptyPane(c)
	})
}

// rowWords names the state of r, a file row.
func (cv *changesState) rowWords(m *changeModel, r changeRow) string {
	if r.kind == rowInner {
		return locales.ChangesState("untracked")
	}
	return entryWords(m.st, int(r.idx), r.sec)
}

// rowLines is the count of r, a file row.
func (cv *changesState) rowLines(m *changeModel, r changeRow) diff.Lines {
	if r.kind == rowInner {
		return m.innerLines(r)
	}
	return cv.entryLines(m.st, int(r.idx), r.sec)
}
