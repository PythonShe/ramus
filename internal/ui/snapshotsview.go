package ui

import (
	"fmt"
	"slices"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/snapshot"
)

// snapState is a tab's recovery view: its snapshots, and the files of
// the one chosen.
type snapState struct {
	// listed is the session's Writes count the list was read at; -1 when
	// it has not been read.
	listed   int
	loading  bool
	list     []snapshot.Snapshot
	err      string
	snapList ui.ListState
	chosen   int

	filesOf  string // ref the files were read for
	files    []snapshot.File
	fileList ui.ListState
	picked   ui.Selection[string]

	ignored bool
}

func newSnapState() snapState { return snapState{listed: -1} }

// snapshotsView lists the snapshots kept in the repository and restores
// all or some of the files of one.
func (a *App) snapshotsView(c *ui.Context, t *tab) {
	th := c.Theme()
	ss := &t.snaps
	a.loadSnapshots(t)
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(8, 12).Gap(12).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Snapshots").Bold().Grow(1)
			ro := t.s.Queue == nil
			ui.Checkbox(c, &ss.ignored, "Include ignored files").Disabled(ro)
			if ui.Button(c, "Snapshot Now").Disabled(ro).Clicked() {
				if _, err := t.s.SnapshotNow(ss.ignored); err != nil {
					a.notice = errText(err)
				}
			}
		})
		if ss.err != "" {
			ui.Text(c, ss.err).TextColor(th.Danger).Padding(0, 12)
		}
		if len(ss.list) == 0 {
			ui.Column(c).Grow(1).Center().Children(func() {
				msg := "No snapshots. One is kept before anything the app does that drops changes."
				if ss.loading {
					msg = "Reading snapshots…"
				}
				ui.Text(c, msg).TextColor(th.TextMuted)
			})
			return
		}
		list := ss.list
		ss.snapList.Selected = &ss.chosen
		ss.snapList.Key = func(i int) any { return list[i].Ref }
		ss.snapList.Label = func(i int) string { return list[i].Reason }
		ui.Split(c, &t.listW, func() {
			ui.List(c, &ss.snapList, len(list), func(i int) {
				s := list[i]
				ui.Column(c).Padding(6, 8).Children(func() {
					ui.Text(c, s.Reason).SingleLine()
					ui.Text(c, s.Time.Format(time.DateTime)).FontSize(12).TextColor(th.TextMuted)
				})
			}).Fill()
		}, func() {
			if ss.chosen < 0 || ss.chosen >= len(list) {
				return
			}
			a.snapshotFiles(c, t, list[ss.chosen])
		}).SplitReset(420).Grow(1)
	})
}

// loadSnapshots reads the list again when a write may have changed it.
func (a *App) loadSnapshots(t *tab) {
	ss := &t.snaps
	if ss.loading || ss.listed == t.s.Writes || t.s.Repo == nil {
		return
	}
	ss.loading = true
	at := t.s.Writes
	t.s.Snapshots(func(list []snapshot.Snapshot, err error) {
		ss.loading, ss.listed = false, at
		ss.err = ""
		if err != nil {
			ss.err = errText(err)
		}
		var keep string
		if ss.chosen >= 0 && ss.chosen < len(ss.list) {
			keep = ss.list[ss.chosen].Ref
		}
		ss.list = list
		ss.chosen = max(0, slices.IndexFunc(list, func(s snapshot.Snapshot) bool { return s.Ref == keep }))
	})
}

// snapshotFiles shows what one snapshot holds and restores it.
func (a *App) snapshotFiles(c *ui.Context, t *tab, s snapshot.Snapshot) {
	th := c.Theme()
	ss := &t.snaps
	if ss.filesOf != s.Ref {
		ss.filesOf, ss.files = s.Ref, nil
		ss.picked.Clear()
		ss.fileList = ui.ListState{}
		ref := s.Ref
		t.s.SnapshotFiles(s, func(files []snapshot.File, err error) {
			if ss.filesOf != ref {
				return
			}
			if err != nil {
				ss.err = errText(err)
			}
			ss.files = files
		})
	}
	files := ss.files
	ui.Column(c).Fill().Padding(12).Gap(8).Children(func() {
		ui.Text(c, s.Reason).Bold()
		var parts string
		if s.Head != "" {
			parts = "Taken on " + s.Head[:min(len(s.Head), 12)]
		}
		if s.Ignored {
			parts += ", ignored files included"
		}
		if parts != "" {
			ui.Text(c, parts).FontSize(12).TextColor(th.TextMuted)
		}
		if files == nil {
			ui.Spinner(c)
			return
		}
		if len(files) == 0 {
			ui.Text(c, "Nothing in it differs from the commit it was taken on").TextColor(th.TextMuted)
			return
		}
		ss.fileList.Key = func(i int) any { return files[i].Path }
		ss.fileList.Selection = &ss.picked
		ss.fileList.Label = func(i int) string { return files[i].Path }
		ui.List(c, &ss.fileList, len(files), func(i int) {
			f := files[i]
			shown, flagged := showPath(f.Path)
			ui.Row(c).Padding(3, 8).Gap(8).Children(func() {
				ui.Text(c, string(rune(f.Status))).Font("monospace").TextColor(th.TextMuted).Width(16)
				name := ui.Text(c, shown).SingleLine().Grow(1)
				if flagged != "" {
					name.TextColor(th.Warning).Description(flagged)
				}
			})
		}).Grow(1)
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			ro := t.s.Queue == nil
			n := ss.picked.Len()
			if ui.Button(c, fmt.Sprintf("Restore Selected (%d)", n)).Disabled(ro || n == 0).Clicked() {
				var paths []string
				for _, f := range files {
					if ss.picked.Has(f.Path) {
						paths = append(paths, f.Path)
					}
				}
				a.restore(t, s, paths)
			}
			if ui.Button(c, "Restore All").Disabled(ro).Clicked() {
				paths := make([]string, len(files))
				for i, f := range files {
					paths[i] = f.Path
				}
				a.restore(t, s, paths)
			}
		})
	})
}

// restore writes files back; what they are now is kept first, so a
// restore is itself undone from this view.
func (a *App) restore(t *tab, s snapshot.Snapshot, paths []string) {
	if _, err := t.s.Restore(s, paths); err != nil {
		a.notice = errText(err)
	}
}

// undo and redo act on the current tab's last write.
func (a *App) undo(t *tab, redo bool) {
	var err error
	if redo {
		_, err = t.s.Redo()
	} else {
		_, err = t.s.Undo()
	}
	if err != nil {
		a.notice = errText(err)
	}
}
