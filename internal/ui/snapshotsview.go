package ui

import (
	"encoding/hex"
	"fmt"
	"slices"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/diff"
	"ramus/internal/git/redact"
	"ramus/internal/git/snapshot"
	"ramus/internal/session"
	"ramus/locales"
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

	filesOf string // ref the files were read for
	// filesRead says the files of filesOf came back, as none when the
	// snapshot holds what HEAD does; filesErr is why they did not.
	filesRead bool
	filesErr  string
	files     []snapshot.File
	fileList  ui.ListState
	picked    ui.Selection[string]
	// fileAt is the file last clicked, whose patch shows beside the
	// snapshot, as a commit's does, -1 for none; infoW is the snapshot's
	// width then.
	fileAt       int
	infoW        float32
	patchFor     string
	patch        session.PatchResult
	pv           patchView
	pager        patchPager
	patchLoading bool
	loadPatch    func()

	ignored bool
	// name is what the next snapshot taken by hand is called; blank, it
	// is named by where it is taken.
	name string
	// ask confirms deleting a snapshot.
	ask confirmState
}

func newSnapState() snapState { return snapState{listed: -1, fileAt: -1, infoW: commitInfoW} }

// snapshotsView lists the snapshots kept in the repository and restores
// all or some of the files of one.
func (a *App) snapshotsView(c *ui.Context, t *tab) {
	th := c.Theme()
	ss := &t.snaps
	a.loadSnapshots(t)
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(barPadY, insetX).Gap(spaceL).AlignItems(ui.Center).Children(func() {
			// The sidebar names the page, as it does the others.
			ui.Spacer(c)
			ro := t.s.Queue == nil
			name := ui.TextInput(c, &ss.name).Label(locales.SnapshotsName()).Placeholder(locales.SnapshotsNamePlaceholder()).Width(220).Disabled(ro)
			ui.Checkbox(c, &ss.ignored, locales.OpsIncludeIgnoredFiles()).Disabled(ro)
			if ui.Button(c, locales.SnapshotsNow()).Disabled(ro).Clicked() || name.Submitted() && !ro {
				if _, err := t.s.SnapshotNow(ss.name, ss.ignored); err != nil {
					a.notice = errText(err)
				} else {
					ss.name = ""
				}
			}
		})
		if ss.err != "" {
			ui.Text(c, ss.err).TextColor(th.Danger).Padding(0, insetX)
		}
		if len(ss.list) == 0 {
			ui.Column(c).Grow(1).Center().Children(func() {
				msg := locales.SnapshotsNone()
				if ss.loading {
					msg = locales.SnapshotsReading()
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
			ui.List(c, &ss.snapList, len(list)).Rows(func(r ui.ListRow) {
				s := list[r.Index]
				ui.Column(c).Padding(rowTallPadY, insetX).Children(func() {
					ui.Text(c, s.Reason).SingleLine()
					ui.Text(c, s.Time.Format(time.DateTime)).FontSize(textSmall).TextColor(rowInk(r).Muted)
				})
			}).Label(locales.SnapshotsSaved()).Fill()
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
		ss.filesOf, ss.files, ss.filesRead, ss.filesErr = s.Ref, nil, false, ""
		ss.picked.Clear()
		ss.fileList = ui.ListState{}
		ss.fileAt, ss.patchFor = -1, ""
		ref := s.Ref
		t.s.SnapshotFiles(s, func(files []snapshot.File, err error) {
			if ss.filesOf != ref {
				return
			}
			ss.filesRead = true
			if err != nil {
				ss.filesErr = errText(err)
			}
			ss.files = files
		})
	}
	files := ss.files
	info := func() {
		ui.Column(c).Fill().Padding(spaceL).Gap(spaceM).Children(func() {
			ui.Row(c).Gap(spaceM).AlignItems(ui.Center).Children(func() {
				ui.Text(c, s.Reason).Bold().Grow(1)
				a.deleteSnapshotButton(c, t, s)
			})
			var parts string
			switch head := s.Head[:min(len(s.Head), 12)]; {
			case head != "" && s.Ignored:
				parts = locales.SnapshotsTakenOnIgnored(head)
			case head != "":
				parts = locales.SnapshotsTakenOn(head)
			case s.Ignored:
				parts = locales.SnapshotsIgnoredIncluded()
			}
			if parts != "" {
				ui.Text(c, parts).FontSize(textSmall).TextColor(th.TextMuted)
			}
			if !ss.filesRead {
				ui.Spinner(c)
				return
			}
			if ss.filesErr != "" {
				ui.Text(c, ss.filesErr).TextColor(th.Danger)
				return
			}
			if len(files) == 0 {
				ui.Text(c, locales.SnapshotsNothingDiffers()).TextColor(th.TextMuted)
				return
			}
			ss.fileList.Key = func(i int) any { return files[i].Path }
			ss.fileList.Selection = &ss.picked
			ss.fileList.Selected = &ss.fileAt
			ss.fileList.Label = func(i int) string { return files[i].Path }
			ui.List(c, &ss.fileList, len(files), func(i int) {
				f := files[i]
				shown, flagged := showPath(f.Path)
				ui.Row(c).Padding(rowPadY, insetX).Gap(spaceM).Children(func() {
					ui.Text(c, string(rune(f.Status))).Font("monospace").TextColor(fileStatusColor(plainInk(th), byte(f.Status))).Width(statusLetterW)
					fileIcon(c, f.Path, plainInk(th))
					name := pathLabel(c, shown).Grow(1)
					if flagged != "" {
						name.TextColor(th.Warning).Description(flagged)
					}
				})
			}).Label(locales.SnapshotsFiles()).Grow(1)
			ui.Row(c).Gap(spaceM).Justify(ui.End).Children(func() {
				ro := t.s.Queue == nil
				n := ss.picked.Len()
				// Restore Selected is there once something is selected: until
				// then it would be a button that cannot be pressed beside the
				// one that can.
				if n > 0 && ui.Button(c, locales.SnapshotsRestoreSelected(n)).Disabled(ro).Clicked() {
					var paths []string
					for _, f := range files {
						if ss.picked.Has(f.Path) {
							paths = append(paths, f.Path)
						}
					}
					a.restore(t, s, paths)
				}
				if ui.Button(c, locales.SnapshotsRestoreAll()).Disabled(ro).Clicked() {
					paths := make([]string, len(files))
					for i, f := range files {
						paths[i] = f.Path
					}
					a.restore(t, s, paths)
				}
			})
		})
	}
	// Until a file is chosen the snapshot takes the column; the file's
	// patch opens in a column of its own beside it, as a commit's does.
	if ss.fileAt < 0 || ss.fileAt >= len(files) {
		info()
		return
	}
	f := files[ss.fileAt]
	ui.Split(c, &ss.infoW, info, func() { a.snapshotPatch(c, t, s, f) }).SplitReset(commitInfoW).Fill()
}

// snapshotPatch shows what snapshot s holds of file f, against the commit
// it was taken on, as its list of files says how f differs.
func (a *App) snapshotPatch(c *ui.Context, t *tab, s snapshot.Snapshot, f snapshot.File) {
	ss := &t.snaps
	ws := diff.Whitespace(a.diffOpts().ws)
	key := fmt.Sprintf("%s\x00%s\x00%d", s.Ref, f.Path, ws)
	if ss.patchFor != key {
		ss.patchFor, ss.patch, ss.pager = key, session.PatchResult{}, patchPager{}
		var detail session.CommitDetail
		detail.OID, _ = hex.DecodeString(s.Commit)
		if head, err := hex.DecodeString(s.Head); err == nil && len(head) > 0 {
			detail.Parents = [][]byte{head}
		}
		paths := []string{f.Path}
		var load func()
		load = func() {
			ss.patchLoading = true
			t.s.CommitPatch(detail, paths, ss.pager.max(), ws, session.Sides{}, func(res session.PatchResult) {
				if ss.patchFor != key {
					return
				}
				ss.patch = res
				a.setPatch(&ss.pv, res)
				if ss.pager.more(res) {
					load()
					return
				}
				ss.patchLoading = false
			})
		}
		ss.loadPatch = load
		load()
	}
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(barPadY, insetX).Gap(spaceM).AlignItems(ui.Center).Children(func() {
			shown, _ := showPath(f.Path)
			pathLabel(c, shown).Bold().Grow(1)
			if closeButton(c, locales.CommitClosePatch()).Clicked() {
				ss.fileAt = -1
			}
		})
		a.diffBar(c, false, nil)
		a.pagerNotice(c, &ss.pager, ss.patch, ss.patchLoading, "", ss.loadPatch)
		a.patchBody(c, t, &ss.pv, ss.patch, nil)
	})
}

// deleteSnapshotButton deletes snapshot s once the user confirms: it is
// the last copy of what it holds.
func (a *App) deleteSnapshotButton(c *ui.Context, t *tab, s snapshot.Snapshot) {
	ss := &t.snaps
	if ui.Button(c, locales.SnapshotsDelete()).Disabled(t.s.Queue == nil).Clicked() {
		ss.ask = confirmState{open: true, title: locales.SnapshotsDeleteTitle(redact.Control(s.Reason)), message: locales.SnapshotsDeleteMessage(),
			button: locales.CommonDelete(), run: func() {
				if _, err := t.s.DeleteSnapshot(s); err != nil {
					a.notice = errText(err)
				}
			}}
	}
	a.confirmView(c, &ss.ask)
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
