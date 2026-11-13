package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/conflict"
	"ramus/internal/git/redact"
	"ramus/internal/git/write"
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
	ui.Modal(c, open, func() { body(ui.Column(c).Width(1100).Height(760)) })
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
	go func() {
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
			mt.ms = mergeState{path: merged, choices: make([]conflict.Choice, n), custom: make([]string, n)}
			mt.ms.cf.Segments = files.Segments
		})
	}()
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
	go func() {
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
	}()
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
		box.Gap(10).Padding(20, 0).Children(func() {
			n, done := len(ms.choices), 0
			for _, ch := range ms.choices {
				if ch != conflict.Unresolved {
					done++
				}
			}
			ui.Row(c).Padding(0, 20).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1).Gap(2).Children(func() {
					ui.Text(c, "Merge "+redact.Control(filepath.Base(mt.merged))).Bold().SingleLine()
					ui.Text(c, redact.Control(mt.merged)).Font("monospace").FontSize(12).TextColor(th.TextMuted).SingleLine().Selectable()
				})
				if n > 0 {
					ui.Text(c, fmt.Sprintf("%d of %d resolved", done, n)).FontSize(12).TextColor(th.TextMuted)
					if ui.Button(c, "All Ours").Clicked() {
						fill(ms.choices, conflict.Ours)
					}
					if ui.Button(c, "All Theirs").Clicked() {
						fill(ms.choices, conflict.Theirs)
					}
				}
				if ui.Button(c, "Cancel").Clicked() {
					a.endMergeTool(false)
				}
				ready := !mt.loading && !mt.saving && mt.files.Segments != nil && done == n
				if ui.PrimaryButton(c, "Save").Disabled(!ready).Clicked() {
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
				ui.Text(c, mt.err).FontSize(12).TextColor(th.Danger).Padding(0, 20).Selectable()
			}
			switch {
			case mt.loading:
				ui.Column(c).Fill().Center().Children(func() { ui.Spinner(c) })
			case mt.files.Binary:
				ui.Column(c).Fill().Center().Gap(12).Children(func() {
					ui.Text(c, "Binary files are merged by keeping one side whole").TextColor(th.TextMuted)
					ui.Row(c).Gap(8).Children(func() {
						for _, side := range []struct{ label, path string }{{"Keep Ours", mt.local}, {"Keep Theirs", mt.remote}} {
							if ui.Button(c, side.label).Disabled(mt.saving).Clicked() {
								a.saveMergeTool(nil, side.path)
							}
						}
					})
				})
			case mt.files.Segments == nil:
			case n == 0:
				ui.Column(c).Fill().Center().Children(func() {
					ui.Text(c, "The sides merge without conflicts").TextColor(th.TextMuted)
				})
			default:
				a.conflictList(c, ms)
			}
		})
	})
}
