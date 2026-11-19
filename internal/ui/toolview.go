package ui

import (
	"context"
	"os"
	"path/filepath"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/conflict"
	"ramus/internal/git/redact"
	"ramus/internal/git/write"
	"ramus/locales"
)

// ToolView is the window of the app run as git's difftool or mergetool:
// the comparison or the merge, and nothing else.
func (a *App) ToolView(c *ui.Context) {
	a.tool = true
	ui.Column(c).Fill().Background(c.Theme().Background).Children(func() {
		a.compareView(c)
		a.mergeToolView(c)
	})
}

// toolFrame holds a comparison or a merge: the whole window in tool mode,
// else a modal over the repositories.
func (a *App) toolFrame(c *ui.Context, open *bool, body func(box ui.Element)) {
	if a.tool {
		body(ui.Column(c).Fill())
		return
	}
	a.modal(c, open, func() { body(ui.Column(c).Width(1100).Height(760)) })
}

// mergeTool resolves the merge git mergetool hands over: the conflicts of
// local and remote over base, written to merged.
type mergeTool struct {
	open    bool
	pending bool
	base    string
	local   string
	remote  string
	merged  string
	loading bool
	files   conflict.Files
	ms      mergeState
	seen    write.FileState
	saving  bool
	err     string
	// done runs once, when the merge is saved (true) or given up.
	done func(resolved bool)
}

// MergeTool shows the merge of local and remote over base (absolute
// paths, base "" for none) to resolve into merged. done, when set, runs
// once it is saved or given up.
func (a *App) MergeTool(base, local, remote, merged string, done func(resolved bool)) {
	a.endMergeTool(false)
	a.toolMerge = mergeTool{open: true, base: base, local: local, remote: remote, merged: merged, loading: true, done: done}
	if a.Runner == nil {
		a.toolMerge.pending = true
		return
	}
	a.startMergeTool()
}

func (a *App) startMergeTool() {
	mt := &a.toolMerge
	mt.pending = false
	r, update := a.Runner, a.Update
	base, local, remote, merged := mt.base, mt.local, mt.remote, mt.merged
	a.goWork(nil, func() {
		seen := write.ObserveMerged(merged)
		labels := [3]string{filepath.Base(local), filepath.Base(base), filepath.Base(remote)}
		if base == "" {
			labels[1] = "base"
		}
		files, err := conflict.MergeFiles(context.Background(), r, base, local, remote, labels)
		update(func() {
			if a.toolMerge.merged != merged || !mt.open {
				return
			}
			mt.loading, mt.files, mt.seen = false, files, seen
			if err != nil {
				mt.err = errText(err)
			}
			n := conflict.Count(files.Segments)
			// gen goes on from the last: a build for that may still land.
			mt.ms = mergeState{path: merged, choices: make([]conflict.Choice, n), custom: make([]string, n), gen: mt.ms.gen}
			mt.ms.cf.Segments = files.Segments
			a.showConflicts(&mt.ms)
		})
	})
}

// endMergeTool closes the merge and tells whoever waits on it.
func (a *App) endMergeTool(resolved bool) {
	mt := &a.toolMerge
	mt.open = false
	if mt.done != nil {
		done := mt.done
		mt.done = nil
		done(resolved)
	}
}

// saveMergeTool writes data, or the file at side when data is nil, to the
// merged file.
func (a *App) saveMergeTool(data []byte, side string) {
	mt := &a.toolMerge
	mt.saving, mt.err = true, ""
	merged, seen, update := mt.merged, mt.seen, a.Update
	a.goWork(nil, func() {
		var err error
		if data == nil {
			data, err = os.ReadFile(side)
		}
		if err == nil {
			err = write.SaveMerged(merged, data, seen)
		}
		update(func() {
			if mt.merged != merged {
				return
			}
			mt.saving = false
			if err != nil {
				mt.err = errText(err)
				return
			}
			a.endMergeTool(true)
		})
	})
}

func (a *App) mergeToolView(c *ui.Context) {
	mt := &a.toolMerge
	if !mt.open {
		if mt.done != nil {
			a.endMergeTool(false)
		}
		return
	}
	th := c.Theme()
	ms := &mt.ms
	a.toolFrame(c, &mt.open, func(box ui.Element) {
		box.Gap(spaceL).Padding(dialogPad, 0).Children(func() {
			n, done := len(ms.choices), 0
			for _, ch := range ms.choices {
				if ch != conflict.Unresolved {
					done++
				}
			}
			ui.Row(c).Padding(0, dialogPad).Gap(spaceM).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1).Gap(space2XS).Children(func() {
					ui.Text(c, locales.MergeToolTitle(redact.Control(filepath.Base(mt.merged)))).Bold().SingleLine()
					ui.Text(c, redact.Control(mt.merged)).Font("monospace").FontSize(textSmall).TextColor(th.TextMuted).SingleLine().Selectable()
				})
				if n > 0 {
					ui.Text(c, locales.MergeResolvedCount(done, n)).FontSize(textSmall).TextColor(th.TextMuted)
					if ui.Button(c, locales.MergeAllOurs()).Clicked() {
						fill(ms.choices, conflict.Ours)
					}
					if ui.Button(c, locales.MergeAllTheirs()).Clicked() {
						fill(ms.choices, conflict.Theirs)
					}
				}
				if ui.Button(c, locales.CommonCancel()).Clicked() {
					a.endMergeTool(false)
				}
				ready := !mt.loading && !mt.saving && mt.files.Segments != nil && done == n
				if ui.PrimaryButton(c, locales.CommonSave()).Disabled(!ready).Clicked() {
					custom := make([][]byte, len(ms.custom))
					for i, s := range ms.custom {
						custom[i] = []byte(s)
					}
					data, err := conflict.Resolve(ms.cf.Segments, ms.choices, custom)
					if err != nil {
						mt.err = errText(err)
					} else {
						a.saveMergeTool(data, "")
					}
				}
			})
			if mt.err != "" {
				ui.Text(c, mt.err).FontSize(textSmall).TextColor(th.Danger).Padding(0, dialogPad).Selectable()
			}
			switch {
			case mt.loading:
				loading(c, "")
			case mt.files.Binary:
				ui.Column(c).Fill().Center().Gap(spaceL).Children(func() {
					ui.Text(c, locales.MergeToolBinary()).TextColor(th.TextMuted)
					ui.Row(c).Gap(spaceM).Children(func() {
						for _, side := range []struct {
							label func() string
							path  string
						}{{locales.MergeKeepOurs, mt.local}, {locales.MergeKeepTheirs, mt.remote}} {
							if ui.Button(c, side.label()).Disabled(mt.saving).Clicked() {
								a.saveMergeTool(nil, side.path)
							}
						}
					})
				})
			case mt.files.Segments == nil:
			case n == 0:
				emptyState(c, locales.MergeToolNoConflicts())
			default:
				a.conflictList(c, ms)
			}
		})
	})
}
