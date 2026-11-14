package ui

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/conflict"
	"ramus/internal/git/redact"
	"ramus/internal/git/repo"
	"ramus/internal/git/write"
	"ramus/internal/session"
)

// mergeState is the merge editor of a tab: one conflicted file, and how
// each of its conflicts is resolved so far.
type mergeState struct {
	path    string
	readW   int
	loading bool
	cf      session.ConflictFile
	choices []conflict.Choice
	custom  []string
	list    ui.ListState
	err     string
	// shown is the text each segment shows, built off the main thread
	// for the load numbered gen: a generated file's conflict can hold
	// tens of thousands of lines.
	shown []shownSegment
	gen   int
	// index numbers each conflict segment among the conflicts.
	index []int
}

// shownSegment is the text a segment shows: unchanged text shortened, or
// each side of a conflict cut to maxShownLines.
type shownSegment struct {
	text  string
	sides [3]string // ours, base, theirs
	// lines is the longest side's line count, to keep Edit off past
	// maxEditLines.
	lines int
}

const (
	// maxShownLines bounds the lines a side of a conflict shows; the
	// choices still take the whole side.
	maxShownLines = 200
	// maxEditLines bounds a conflict edited in place.
	maxEditLines = 5000
)

// showSegments builds what each segment shows.
func showSegments(segs []conflict.Segment) []shownSegment {
	out := make([]shownSegment, len(segs))
	for i, s := range segs {
		if s.Conflict == nil {
			out[i].text = compact(s.Text)
			continue
		}
		cc := s.Conflict
		for k, side := range [][]byte{cc.Ours, cc.Base, cc.Theirs} {
			var n int
			out[i].sides[k], n = headLines(side, maxShownLines)
			out[i].lines = max(out[i].lines, n)
		}
	}
	return out
}

// headLines is text's first n lines, with how many follow, and its line
// count.
func headLines(text []byte, n int) (string, int) {
	total := bytes.Count(text, []byte{'\n'})
	if len(text) > 0 && text[len(text)-1] != '\n' {
		total++
	}
	cut := text
	if total > n {
		end := 0
		for range n {
			end += bytes.IndexByte(text[end:], '\n') + 1
		}
		cut = text[:end]
	}
	s := strings.TrimSuffix(redact.Control(string(cut)), "\n")
	if total > n {
		s += fmt.Sprintf("\n⋯ %d more lines", total-n)
	}
	return s, total
}

// mergeChoices are the choices offered for a conflict, in order.
var mergeChoices = []struct {
	label  string
	choice conflict.Choice
}{
	{"Ours", conflict.Ours},
	{"Theirs", conflict.Theirs},
	{"Both", conflict.OursThenTheirs},
	{"Both, Theirs First", conflict.TheirsThenOurs},
	{"Base", conflict.Base},
	{"Edit", conflict.Custom},
}

func (a *App) loadMerge(t *tab, path string) {
	ms := &t.merge
	if ms.loading || ms.path == path && ms.readW == t.s.Writes {
		return
	}
	ms.path, ms.readW, ms.loading, ms.err = path, t.s.Writes, true, ""
	t.s.LoadConflict(path, func(cf session.ConflictFile) {
		if ms.path != path {
			ms.loading = false
			return
		}
		ms.loading, ms.cf = false, cf
		n := conflict.Count(cf.Segments)
		ms.choices, ms.custom = make([]conflict.Choice, n), make([]string, n)
		a.showConflicts(ms)
	})
}

// showConflicts numbers ms's conflicts and builds what its segments show
// off the main thread.
func (a *App) showConflicts(ms *mergeState) {
	ms.gen++
	gen, segs, update := ms.gen, ms.cf.Segments, a.Update
	ms.shown, ms.index = nil, make([]int, len(segs))
	k := 0
	for i, s := range segs {
		ms.index[i] = k
		if s.Conflict != nil {
			k++
		}
	}
	go func() {
		shown := showSegments(segs)
		update(func() {
			if ms.gen == gen {
				ms.shown = shown
			}
		})
	}()
}

// mergeEditor resolves a conflicted file: each conflict by a side, both,
// the base or text of the user's own, or a binary file by a side.
func (a *App) mergeEditor(c *ui.Context, t *tab, path string) {
	th := c.Theme()
	ms := &t.merge
	a.loadMerge(t, path)
	cf := ms.cf
	ro := t.s.Queue == nil
	w := t.s.Where()
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(6, 12).Gap(8).AlignItems(ui.Center).Children(func() {
			shown, _ := showPath(path)
			ui.Text(c, shown).Bold().SingleLine().Grow(1)
			if cf.Path != path || ms.loading && cf.Segments == nil {
				return
			}
			if n := len(ms.choices); n > 0 {
				done := 0
				for _, ch := range ms.choices {
					if ch != conflict.Unresolved {
						done++
					}
				}
				ui.Text(c, fmt.Sprintf("%d of %d resolved", done, n)).FontSize(12).TextColor(th.TextMuted)
				if ui.Button(c, "All Ours").Clicked() {
					fill(ms.choices, conflict.Ours)
				}
				if ui.Button(c, "All Theirs").Clicked() {
					fill(ms.choices, conflict.Theirs)
				}
				if ui.PrimaryButton(c, "Save and Mark Resolved").Disabled(ro || done < n).Clicked() {
					a.saveMerge(t)
				}
			}
		})
		if ms.err != "" {
			ui.Text(c, ms.err).FontSize(12).TextColor(th.Danger).Padding(0, 12).Selectable()
		}
		switch {
		case cf.Path != path:
			ui.Column(c).Fill().Center().Children(func() { ui.Spinner(c) })
			return
		case !cf.Ours || !cf.Theirs || cf.Binary:
			a.sidesOnly(c, t, cf)
			return
		case cf.Err != nil:
			ui.Text(c, errText(cf.Err)).TextColor(th.Danger).Padding(12).Selectable()
			return
		case len(ms.choices) == 0:
			ui.Column(c).Fill().Center().Gap(12).Children(func() {
				ui.Text(c, "No conflict markers are left in this file").TextColor(th.TextMuted)
				if ui.Button(c, "Mark Resolved").Disabled(ro).Clicked() {
					a.submit(t, write.Stage(w, []string{path}), nil)
				}
			})
			return
		}
		a.conflictList(c, ms)
	})
}

// conflictList is the segments of a conflicted file: the text the sides
// agree on, shortened, and each conflict with its choices.
func (a *App) conflictList(c *ui.Context, ms *mergeState) {
	th := c.Theme()
	segs := ms.cf.Segments
	if ms.shown == nil {
		ui.Column(c).Fill().Center().Children(func() { ui.Spinner(c) })
		return
	}
	ms.list.Label = func(i int) string {
		if segs[i].Conflict != nil {
			return "Conflict " + strconv.Itoa(ms.index[i]+1)
		}
		return "Unchanged text"
	}
	ui.List(c, &ms.list, len(segs), func(i int) {
		s := segs[i]
		if s.Conflict == nil {
			ui.Text(c, ms.shown[i].text).Font("monospace").FontSize(12).TextColor(th.TextMuted).Padding(4, 12).FillWidth()
			return
		}
		a.conflictBlock(c, ms, ms.index[i], s.Conflict, ms.shown[i])
	}).Label("Conflicts and unchanged text").Grow(1)
}

func fill(cs []conflict.Choice, ch conflict.Choice) {
	for i := range cs {
		cs[i] = ch
	}
}

func (a *App) conflictBlock(c *ui.Context, ms *mergeState, k int, cc *conflict.Conflict, shown shownSegment) {
	th := c.Theme()
	ui.Column(c).Padding(8, 12).Gap(8).FillWidth().Background(th.Surface).Children(func() {
		ui.Text(c, "Conflict "+strconv.Itoa(k+1)).Bold()
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			for _, mc := range mergeChoices {
				if mc.choice == conflict.Base && !cc.HasBase {
					continue
				}
				var b ui.Element
				if ms.choices[k] == mc.choice {
					b = ui.PrimaryButton(c, mc.label)
				} else {
					b = ui.Button(c, mc.label)
				}
				if mc.choice == conflict.Custom && shown.lines > maxEditLines {
					b.Disabled(true).Tooltip(fmt.Sprintf("Over %d lines: edit this conflict in your editor", maxEditLines))
				}
				if b.Clicked() {
					if mc.choice == conflict.Custom && ms.choices[k] != conflict.Custom {
						ms.custom[k] = string(resolvedText(cc, ms.choices[k]))
					}
					ms.choices[k] = mc.choice
				}
			}
		})
		ui.Row(c).Gap(8).FillWidth().Children(func() {
			side := func(title string, text string) {
				ui.Column(c).Grow(1).Gap(4).Children(func() {
					ui.Text(c, title).FontSize(12).TextColor(th.TextMuted).SingleLine()
					ui.Text(c, text).Font("monospace").FontSize(12).Selectable()
				})
			}
			side("Ours: "+redact.Control(cc.OursLabel), shown.sides[0])
			if cc.HasBase {
				side("Base: "+redact.Control(cc.BaseLabel), shown.sides[1])
			}
			side("Theirs: "+redact.Control(cc.TheirsLabel), shown.sides[2])
		})
		if ms.choices[k] == conflict.Custom {
			ui.TextArea(c, &ms.custom[k]).Font("monospace").FontSize(12).Label("Result of conflict " + strconv.Itoa(k+1)).Height(120)
		}
	})
}

// resolvedText is what choice keeps of a conflict; the ours side when
// nothing is chosen yet, as a start to edit.
func resolvedText(cc *conflict.Conflict, ch conflict.Choice) []byte {
	if ch == conflict.Unresolved || ch == conflict.Custom {
		ch = conflict.Ours
	}
	out, _ := conflict.Resolve([]conflict.Segment{{Conflict: cc}}, []conflict.Choice{ch}, nil)
	return out
}

// compact shows unchanged text around conflicts: a short run whole, a long
// one by its first and last lines.
func compact(text []byte) string {
	lines := bytes.SplitAfter(text, []byte{'\n'})
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	if len(lines) <= 7 {
		return strings.TrimSuffix(redact.Control(string(text)), "\n")
	}
	head := bytes.Join(lines[:3], nil)
	tail := bytes.Join(lines[len(lines)-3:], nil)
	return redact.Control(string(head)) + fmt.Sprintf("⋯ %d lines\n", len(lines)-6) + strings.TrimSuffix(redact.Control(string(tail)), "\n")
}

func (a *App) saveMerge(t *tab) {
	ms := &t.merge
	custom := make([][]byte, len(ms.custom))
	for i, s := range ms.custom {
		custom[i] = []byte(s)
	}
	data, err := conflict.Resolve(ms.cf.Segments, ms.choices, custom)
	if err == nil {
		var op write.Op
		op, err = write.Resolve(t.s.Where(), ms.path, data, ms.cf.Seen)
		if err == nil {
			_, err = t.s.Submit(op)
		}
	}
	ms.err = ""
	if err != nil {
		ms.err = errText(err)
	}
}

// sidesOnly resolves a file that cannot be merged line by line: binary,
// or deleted on one side.
func (a *App) sidesOnly(c *ui.Context, t *tab, cf session.ConflictFile) {
	th := c.Theme()
	ro := t.s.Queue == nil
	w := t.s.Where()
	paths := []string{cf.Path}
	ui.Column(c).Fill().Center().Gap(12).Children(func() {
		var what string
		switch {
		case !cf.Ours:
			what = "Deleted on this side, changed on the other"
		case !cf.Theirs:
			what = "Changed on this side, deleted on the other"
		default:
			what = "Binary file changed on both sides"
		}
		ui.Text(c, what).TextColor(th.TextMuted)
		ui.Row(c).Gap(8).Children(func() {
			for _, s := range []struct {
				label  string
				side   write.Side
				exists bool
			}{{"Take Ours", write.OurSide, cf.Ours}, {"Take Theirs", write.TheirSide, cf.Theirs}} {
				label := s.label
				if !s.exists {
					label = "Delete It"
				}
				if ui.Button(c, label).Disabled(ro).Clicked() {
					if s.exists {
						op, err := write.TakeSide(w, paths, s.side)
						a.submit(t, op, err)
					} else {
						op, err := write.RemoveConflicted(w, paths)
						a.submit(t, op, err)
					}
				}
			}
		})
	})
}

// takeBoth resolves every conflict in path keeping both sides, ours
// first.
func (a *App) takeBoth(t *tab, path string) {
	t.s.LoadConflict(path, func(cf session.ConflictFile) {
		err := cf.Err
		if err == nil && (cf.Binary || cf.Segments == nil) {
			err = fmt.Errorf("%s cannot be merged line by line", path)
		}
		if err == nil {
			cs := make([]conflict.Choice, conflict.Count(cf.Segments))
			fill(cs, conflict.OursThenTheirs)
			var data []byte
			if data, err = conflict.Resolve(cf.Segments, cs, nil); err == nil {
				var op write.Op
				op, err = write.Resolve(t.s.Where(), path, data, cf.Seen)
				a.submit(t, op, err)
				return
			}
		}
		a.notice = errText(err)
	})
}

// operationBar shows an operation that stopped part way, and what can be
// done about it.
func (a *App) operationBar(c *ui.Context, t *tab) {
	op := t.s.State.Operation
	switch op {
	case repo.Merge, repo.Rebase, repo.CherryPick, repo.Revert, repo.Am:
	default:
		return
	}
	th := c.Theme()
	ro := t.s.Queue == nil
	conflicts := 0
	if st := t.s.State.Status; st != nil {
		conflicts = st.Unmerged()
	}
	w := t.s.Where()
	name := string(op)
	ui.Row(c).Padding(6, 12).Gap(8).AlignItems(ui.Center).Background(th.Surface).Children(func() {
		what := strings.ToUpper(name[:1]) + name[1:] + " stopped"
		if conflicts > 0 {
			what += ": " + plural(conflicts, "conflicted file") + " left"
		}
		ui.Text(c, what).Bold().SingleLine().Grow(1)
		if ui.Button(c, "Continue").Disabled(ro || conflicts > 0).Clicked() {
			o, err := write.Sequence(w, name, write.Continue)
			a.submit(t, o, err)
		}
		if op != repo.Merge {
			if ui.Button(c, "Skip").Disabled(ro).Clicked() {
				o, err := write.Sequence(w, name, write.Skip)
				a.submit(t, o, err)
			}
		}
		if ui.Button(c, "Abort…").Disabled(ro).Clicked() {
			o, err := write.Sequence(w, name, write.Abort)
			if err != nil {
				a.notice = errText(err)
				return
			}
			a.confirm = confirmState{open: true, title: "Abort the " + name + "?",
				message: "Files go back to where they were before it; resolutions made so far are kept in a snapshot.",
				button:  "Abort", run: func() { a.submit(t, o, nil) }}
		}
	})
}

// mergeDialog asks how to merge a branch, after showing what it would
// do.
type mergeDialog struct {
	open    bool
	t       *tab
	what    string
	mode    int
	preview *session.MergePreview
	err     string
	list    ui.ListState
}

func (a *App) openMerge(t *tab, what string) {
	a.mergeUI = mergeDialog{open: true, t: t, what: what}
	d := &a.mergeUI
	t.s.PreviewMerge(what, func(p session.MergePreview, err error) {
		if d.t != t || d.what != what {
			return
		}
		d.preview = &p
		if err != nil {
			d.err = errText(err)
		}
	})
}

var mergeModes = []write.MergeMode{write.MergeDefault, write.MergeFFOnly, write.MergeNoFF, write.MergeSquash}

func (a *App) mergeDialogView(c *ui.Context) {
	d := &a.mergeUI
	if !d.open || d.t == nil {
		return
	}
	th := c.Theme()
	t := d.t
	head := t.s.State.Head
	into := "HEAD"
	if !head.Detached {
		into = strings.TrimPrefix(head.Target, "refs/heads/")
	}
	ui.Modal(c, &d.open, func() {
		ui.Column(c).Width(520).Height(420).Gap(12).Padding(20).Children(func() {
			ui.Text(c, "Merge "+redact.Control(strings.TrimPrefix(d.what, "refs/heads/"))+" into "+redact.Control(into)).Bold()
			ui.Segmented(c, &d.mode, "Fast-Forward If Possible", "Fast-Forward Only", "Always Merge Commit", "Squash")
			p := d.preview
			switch {
			case d.err != "":
				ui.Text(c, d.err).TextColor(th.Danger).Selectable()
			case p == nil:
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Spinner(c)
					ui.Text(c, "Working out the merge").TextColor(th.TextMuted)
				})
			case p.UpToDate:
				ui.Text(c, "Already up to date").TextColor(th.TextMuted)
			case len(p.Conflicts) == 0:
				ui.Text(c, "Merges cleanly; "+plural(len(p.BothChanged), "file")+" changed on both sides").TextColor(th.TextMuted)
			default:
				ui.Text(c, plural(len(p.Conflicts), "file")+" will conflict").TextColor(th.Warning)
			}
			if p != nil && len(p.BothChanged) > 0 {
				conflicts := map[string]bool{}
				for _, f := range p.Conflicts {
					conflicts[f] = true
				}
				files := p.BothChanged
				d.list.Label = func(i int) string { return files[i] }
				ui.List(c, &d.list, len(files), func(i int) {
					ui.Row(c).Padding(2, 4).Gap(8).FillWidth().Children(func() {
						shown, _ := showPath(files[i])
						ui.Text(c, shown).Font("monospace").FontSize(12).SingleLine().Grow(1)
						if conflicts[files[i]] {
							ui.Text(c, "conflict").FontSize(12).TextColor(th.Warning)
						} else {
							ui.Text(c, "merges").FontSize(12).TextColor(th.TextMuted)
						}
					})
				}).Label("Files that would conflict").Grow(1)
			} else {
				ui.Spacer(c)
			}
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				if ui.Button(c, "Cancel").Clicked() {
					d.open = false
				}
				if ui.PrimaryButton(c, "Merge").Disabled(t.s.Queue == nil || p != nil && p.UpToDate).Clicked() {
					op, err := write.Merge(t.s.Where(), d.what, mergeModes[d.mode], t.s.Seen())
					a.submit(t, op, err)
					d.open = false
				}
			})
		})
	})
}

// conflictMenu adds what resolves conflicted files whole to their menu.
func (a *App) conflictMenu(menu *ui.Menu, t *tab, m *changeModel, es []int32, what string, act *func()) {
	ro := t.s.Queue == nil || len(es) == 0
	w := t.s.Where()
	// A side that deleted a file is taken by deleting it.
	split := func(side write.Side) (take, remove []string) {
		for _, e := range es {
			x, y := m.st.XY(int(e))
			gone := x == 'D'
			if side == write.TheirSide {
				gone = y == 'D'
			}
			p := string(m.st.Path(int(e)))
			if gone {
				remove = append(remove, p)
			} else {
				take = append(take, p)
			}
		}
		return take, remove
	}
	for _, s := range []struct {
		label string
		side  write.Side
	}{{"Take Ours for ", write.OurSide}, {"Take Theirs for ", write.TheirSide}} {
		if menu.Item(s.label + what).Disabled(ro).Chosen() {
			*act = func() {
				take, remove := split(s.side)
				if len(take) > 0 {
					op, err := write.TakeSide(w, take, s.side)
					a.submit(t, op, err)
				}
				if len(remove) > 0 {
					op, err := write.RemoveConflicted(w, remove)
					a.submit(t, op, err)
				}
			}
		}
	}
	if len(es) == 1 {
		p := string(m.st.Path(int(es[0])))
		if menu.Item("Take Both, Ours First").Disabled(ro).Chosen() {
			*act = func() { a.takeBoth(t, p) }
		}
	}
	gui, tool := "", ""
	if r := t.s.Repo; r != nil {
		gui, _ = r.Config().Get("merge.guitool")
		tool, _ = r.Config().Get("merge.tool")
	}
	if menu.Item("Open in Merge Tool").Disabled(ro || gui == "" && tool == "").Chosen() {
		*act = func() {
			var paths []string
			for _, e := range es {
				paths = append(paths, string(m.st.Path(int(e))))
			}
			op, err := write.MergeTool(w, paths, gui != "")
			a.submit(t, op, err)
		}
	}
	menu.Separator()
}
