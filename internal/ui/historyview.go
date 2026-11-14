package ui

import (
	"encoding/hex"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/history"
	"ramus/internal/git/lanes"
	"ramus/internal/git/refs"
	"ramus/internal/session"
	"ramus/internal/ui/graph"
	"ramus/locales"
)

const (
	historyRowHeight = rowHeight
	// maxGraphWidth bounds the graph column; lanes past it are cut off.
	maxGraphWidth = 160
	// historyTextBudget bounds the row text kept for rows scrolled past.
	historyTextBudget = 8 << 20
	// textReads bounds the chunk reads in flight while scrolling fast.
	textReads = 4
)

// historyState is a tab's history list and the commit chosen in it.
type historyState struct {
	list   ui.ListState
	chosen int
	// textFor is the load the text chunks were read for: rows of another
	// load are other commits.
	textFor histLoad
	text    map[int]*history.TextChunk
	// textBudget, when set, replaces historyTextBudget.
	textBudget int
	used       map[int]uint64
	asked      map[int]bool
	inflight   int
	frame      uint64

	// labels are built for the refs and the history (its stashes and
	// tags) they were built from.
	labelsFor, labelsHist *refs.List
	labels                map[string][]refLabel

	// want is a commit to choose once it is found in the list: the one
	// chosen before history reloaded, or one jumped to.
	want    []byte
	wantFor *lanes.Snapshot
	// writing is set once a commit-graph write was asked for here.
	writing bool
	jump    string
	jumpErr string
	// jumpN numbers the go-to requests; only the latest is answered.
	jumpN int
	buf   []lanes.Edge
	// picked are the commits chosen together, by commit index.
	picked  ui.Selection[uint32]
	path    ui.Path
	detail  commitView
	detailW float32
	// rowW is the list's width as last laid out, 0 before.
	rowW float32
	// cols are the columns as laid out in this frame; colDrag a divider
	// being dragged.
	cols     histLayout
	colDrag  colDrag
	widthKey listWidthKey
	chosenID []byte
	filter   historyFilter
}

// listWidthKey is what sets the width of the history list: the window's,
// the sidebar's and the commit's beside it.
type listWidthKey struct {
	window, side, sideOut float32
	noSidebar             bool
	split, listW          float32
	patch                 bool
}

// histLoad names one load of history: the refs and the view it was for.
type histLoad struct {
	refs *refs.List
	view int
}

func loadOf(h session.HistoryState) histLoad { return histLoad{h.For, h.View} }

func newHistoryState() historyState {
	return historyState{chosen: -1, detailW: 640, detail: commitView{}, text: map[int]*history.TextChunk{}, used: map[int]uint64{}, asked: map[int]bool{}}
}

type labelKind uint8

const (
	labelBranch labelKind = iota
	labelCurrent
	labelRemote
	labelTag
	labelHead
	labelStash
	labelBisectBad
	labelBisectGood
	labelBisectSkip
)

// refLabel is a ref at a commit: text is its name, "" for a bisect mark,
// which name says in the language shown.
type refLabel struct {
	text string
	kind labelKind
}

func (l refLabel) name() string {
	switch l.kind {
	case labelBisectBad:
		return locales.HistoryBisectBad()
	case labelBisectGood:
		return locales.HistoryBisectGood()
	case labelBisectSkip:
		return locales.HistoryBisectSkipped()
	}
	return l.text
}

// historyView is the history list beside the chosen commit.
func (a *App) historyView(c *ui.Context, t *tab) {
	hv := &t.hist
	a.loadRefPrefs(t)
	t.s.LoadHistory()
	h := t.s.History
	hv.frame++
	if hv.textFor != loadOf(h) {
		if hv.textFor.refs != nil && hv.chosenID != nil {
			hv.want, hv.wantFor = hv.chosenID, nil
		}
		hv.textFor, hv.chosen, hv.writing = loadOf(h), -1, false
		clear(hv.text)
		clear(hv.used)
		clear(hv.asked)
	}
	a.findWanted(t, h)
	a.buildLabels(hv, t.s.State, h)
	listBesideCommit(c, &hv.detailW, 640, &hv.detail, func() {
		ui.Column(c).Fill().Children(func() {
			a.bisectBar(c, t)
			a.historyBar(c, t, h)
			switch {
			case h.Snap == nil && h.Err == nil:
				loading(c, "")
				return
			case h.Snap == nil || h.Snap.Len() == 0:
				msg := locales.HistoryNoCommits()
				if h.Filtered {
					msg = locales.HistoryNoMatch()
				}
				if h.Err != nil {
					msg = errText(h.Err)
				}
				emptyState(c, msg)
				return
			}
			a.historyList(c, t, h)
		})
	}, func() { a.commitDetail(c, t, hv.chosenID, &hv.detail) }).Fill()
	if hv.chosen >= 0 && h.Snap != nil && hv.chosen < h.Snap.Len() {
		id := h.OID(h.Snap.Commit(hv.chosen))
		if string(id) != string(hv.chosenID) {
			// A row chosen by hand outranks a go-to still resolving. A
			// copy: id may point into the commit-graph's mapping, which
			// goes when the repository reopens.
			hv.chosenID = slices.Clone(id)
			hv.jumpN++
		}
	}
}

func (a *App) historyBar(c *ui.Context, t *tab, h session.HistoryState) {
	th := c.Theme()
	hv := &t.hist
	ui.Row(c).Padding(barPadY, insetX).Gap(spaceM).AlignItems(ui.Center).Children(func() {
		in := ui.TextInput(c, &hv.jump).Placeholder(locales.HistoryGoTo()).Label(locales.HistoryGoTo()).Grow(1)
		if in.Submitted() && strings.TrimSpace(hv.jump) != "" {
			a.jumpTo(t, strings.TrimSpace(hv.jump))
		}
		if hv.jumpErr != "" {
			ui.Text(c, hv.jumpErr).FontSize(textSmall).TextColor(th.Danger).SingleLine()
		}
		ui.Text(c, historyStatus(h)).FontSize(textSmall).TextColor(th.TextMuted).SingleLine()
		a.filterButton(c, t)
	})
	a.filterBar(c, t)
	if h.Advice != nil && h.Done {
		ui.Row(c).Padding(bannerPadY, insetX).Gap(spaceM).AlignItems(ui.Center).Background(th.Surface).Children(func() {
			ui.Text(c, h.Advice.Reason).FontSize(textSmall).Grow(1)
			if ui.Button(c, locales.HistoryWriteCommitGraph()).Disabled(hv.writing).Clicked() {
				if err := t.s.WriteCommitGraph(*h.Advice); err != nil {
					a.notice = errText(err)
				} else {
					hv.writing = true
				}
			}
		})
	}
}

func historyStatus(h session.HistoryState) string {
	if h.Snap == nil {
		return ""
	}
	n := h.Snap.Len()
	switch {
	case !h.Done && h.Filtered:
		return locales.HistorySearching(n)
	case !h.Done:
		return locales.HistoryCommitsSoFar(n)
	}
	return locales.HistoryCommitCount(n)
}

// jumpTo chooses the commit rev names: in the list when history reaches
// it, and in the detail either way.
func (a *App) jumpTo(t *tab, rev string) {
	hv := &t.hist
	hv.jumpErr = ""
	hv.jumpN++
	n := hv.jumpN
	t.s.ResolveCommit(rev, func(id []byte, err error) {
		if n != hv.jumpN {
			return // a newer request, or the user chose a commit meanwhile
		}
		if err != nil {
			hv.jumpErr = errText(err)
			return
		}
		a.chooseCommit(t, id)
	})
}

// chooseCommit goes to commit id: a page of its own, which Back returns
// from.
func (a *App) chooseCommit(t *tab, id []byte) {
	t.nav.jump = true
	a.showCommit(t, id)
}

// showCommit shows commit id in the detail and chooses its row once it
// is found.
func (a *App) showCommit(t *tab, id []byte) {
	hv := &t.hist
	hv.jumpN++
	hv.want, hv.wantFor = id, nil
	hv.chosenID = id
	hv.chosen = -1
}

// findWanted looks for the wanted commit in each new snapshot, so a
// commit chosen before history reloaded, or not yet loaded, is chosen
// when it shows up.
func (a *App) findWanted(t *tab, h session.HistoryState) {
	hv := &t.hist
	if hv.want == nil || h.Snap == nil || hv.wantFor == h.Snap {
		return
	}
	want, snap := hv.want, h.Snap
	hv.wantFor = snap
	t.s.FindRow(h, want, func(row int) {
		if row < 0 || string(hv.want) != string(want) {
			if t.s.History.Done && t.s.History.Snap == snap && string(hv.want) == string(want) {
				hv.want = nil
			}
			return
		}
		if loadOf(t.s.History) != hv.textFor {
			return
		}
		hv.want, hv.chosen, hv.chosenID = nil, row, want
		hv.list.ScrollTo(row, ui.Center)
	})
}

// buildLabels maps commits to the refs that point at them, when the refs
// change.
//
// They are built off the main thread: tens of thousands of refs take
// milliseconds. The labels shown stay until the new ones come.
func (a *App) buildLabels(hv *historyState, st session.State, h session.HistoryState) {
	if st.Refs == hv.labelsFor && h.For == hv.labelsHist {
		return
	}
	hv.labelsFor, hv.labelsHist = st.Refs, h.For
	refs, hist, update := st.Refs, h.For, a.Update
	go func() {
		labels := labelMap(st, h)
		update(func() {
			if hv.labelsFor == refs && hv.labelsHist == hist {
				hv.labels = labels
			}
		})
	}()
}

// labelMap maps each commit to the labels of the refs at it.
func labelMap(st session.State, h session.HistoryState) map[string][]refLabel {
	labels := map[string][]refLabel{}
	add := func(id []byte, l refLabel) {
		if len(id) > 0 {
			labels[string(id)] = append(labels[string(id)], l)
		}
	}
	if st.Head.Detached {
		add(st.Head.OID, refLabel{"HEAD", labelHead})
	}
	if list := st.Refs; list != nil {
		for i := range list.Len() {
			name := list.Name(i)
			switch {
			case strings.HasPrefix(name, "refs/heads/"):
				kind := labelBranch
				if name == st.Head.Target && !st.Head.Detached {
					kind = labelCurrent
				}
				add(list.OID(i), refLabel{strings.TrimPrefix(name, "refs/heads/"), kind})
			case strings.HasPrefix(name, "refs/remotes/"):
				if list.Symbolic(i) == "" {
					add(list.OID(i), refLabel{strings.TrimPrefix(name, "refs/remotes/"), labelRemote})
				}
			case strings.HasPrefix(name, "refs/tags/"):
				id := list.Peeled(i)
				if id == nil {
					id = h.TagCommits[string(list.OID(i))]
				}
				if id == nil {
					id = list.OID(i)
				}
				add(id, refLabel{strings.TrimPrefix(name, "refs/tags/"), labelTag})
			case name == "refs/bisect/bad":
				add(list.OID(i), refLabel{"", labelBisectBad})
			case strings.HasPrefix(name, "refs/bisect/good-"):
				add(list.OID(i), refLabel{"", labelBisectGood})
			case strings.HasPrefix(name, "refs/bisect/skip-"):
				add(list.OID(i), refLabel{"", labelBisectSkip})
			}
		}
	}
	for i, s := range h.Stashes {
		add(s.Base, refLabel{"stash@{" + strconv.Itoa(i) + "}", labelStash})
	}
	return labels
}

// row returns the text of row i, asking for its chunk when it is not
// read yet.
func (a *App) historyRow(t *tab, h session.HistoryState, i int) (subject, author string, when time.Time, ok bool) {
	hv := &t.hist
	k := i / history.TextChunkRows
	ch := hv.text[k]
	if ch == nil {
		if !hv.asked[k] && hv.inflight < textReads {
			hv.asked[k] = true
			hv.inflight++
			first := k * history.TextChunkRows
			n := min(history.TextChunkRows, h.Snap.Len()-first)
			// A chunk at the end of a list still loading is read again
			// once the list has grown past it.
			partial := n < history.TextChunkRows && !h.Done
			For := loadOf(h)
			t.s.HistoryText(h, first, n, func(ch *history.TextChunk, err error) {
				hv.inflight--
				if hv.textFor != For {
					return
				}
				if err != nil || partial {
					delete(hv.asked, k)
					if err != nil {
						a.notice = errText(err)
						return
					}
				}
				hv.text[k], hv.used[k] = ch, hv.frame
				hv.evict()
			})
		}
		return "", "", time.Time{}, false
	}
	if i-ch.First >= ch.Len() {
		// A chunk read when the list was shorter.
		if !hv.asked[k] {
			delete(hv.text, k)
		}
		return "", "", time.Time{}, false
	}
	hv.used[k] = hv.frame
	s, au, w := ch.Row(i)
	return s, au, w, true
}

// loaded is the chunk holding row i's text, nil until it is read.
func (hv *historyState) loaded(i int) *history.TextChunk {
	ch := hv.text[i/history.TextChunkRows]
	if ch == nil || i-ch.First >= ch.Len() {
		return nil
	}
	return ch
}

// historyMerged is the abbreviated parents of row i when it is a merge
// and its text is loaded, else "".
func (hv *historyState) historyMerged(i int) string {
	if ch := hv.loaded(i); ch != nil {
		return ch.Merged(i)
	}
	return ""
}

// evict drops the chunks used longest ago past the text budget.
func (hv *historyState) evict() {
	total := 0
	for _, c := range hv.text {
		total += c.Bytes()
	}
	budget := historyTextBudget
	if hv.textBudget > 0 {
		budget = hv.textBudget
	}
	for total > budget && len(hv.text) > 1 {
		oldest, at := -1, ^uint64(0)
		for k := range hv.text {
			if hv.used[k] < at {
				oldest, at = k, hv.used[k]
			}
		}
		total -= hv.text[oldest].Bytes()
		delete(hv.text, oldest)
		delete(hv.asked, oldest)
		delete(hv.used, oldest)
	}
}

func (a *App) historyList(c *ui.Context, t *tab, h session.HistoryState) {
	hv := &t.hist
	snap := h.Snap
	hv.list.Selected = &hv.chosen
	hv.list.Selection = &hv.picked
	hv.list.Key = func(i int) any { return snap.Commit(i) }
	hv.list.DragRows = nil
	if t.s.Queue != nil {
		hv.list.DragRows = func(rows []int) any {
			ids := make(draggedCommits, len(rows))
			for k, r := range rows {
				ids[k] = hex.EncodeToString(h.OID(snap.Commit(r)))
			}
			return ids
		}
	}
	hv.list.Label = func(i int) string {
		s, au, w, ok := a.historyRow(t, h, i)
		if !ok {
			return locales.HistoryRowLoading()
		}
		date := w.Format("2006-01-02 15:04")
		out := locales.HistoryRow(s, au, date)
		if m := hv.historyMerged(i); m != "" {
			out = locales.HistoryRowMerge(m, s, au, date)
		}
		if labels := hv.labels[string(h.OID(snap.Commit(i)))]; len(labels) > 0 {
			names := make([]string, len(labels))
			for j, l := range labels {
				names[j] = locales.HistoryRefNamed(l.kind.word(), l.name())
			}
			out = locales.HistoryRowAt(out, strings.Join(names, ", "))
		}
		return out
	}
	a.historyHeader(c, hv, snap)
	l := ui.List(c, &hv.list, snap.Len(), func(i int) { a.historyRowView(c, t, h, i) }).Label(locales.HistoryCommits()).Grow(1)
	// The list's width is known once it is laid out, after the columns
	// were: when what sets it changes, a frame more measures it, and one
	// more lays the columns out for it.
	ww, _ := c.Size()
	key := listWidthKey{ww, t.sideW, t.sideOut, a.Settings.NoSidebar, hv.detailW, hv.detail.listW, hv.detail.patchOpen()}
	if w := l.LayoutBounds().W; key != hv.widthKey || w != hv.rowW {
		hv.widthKey, hv.rowW = key, w
		c.AnimationFrame()
	}
}

func (a *App) historyRowView(c *ui.Context, t *tab, h session.HistoryState, i int) {
	th := c.Theme()
	hv := &t.hist
	snap := h.Snap
	subject, author, when, ok := a.historyRow(t, h, i)
	id := h.OID(snap.Commit(i))
	lay := &hv.cols
	gw := lay.w[colGraph]
	cols := graph.DefaultStyle.Columns(gw)
	r, buf := snap.Row(i, cols, hv.buf)
	hv.buf = buf
	merge := r.Merges > 0
	// No gap: each column's opens and closes with it, as the header's.
	row := ui.Row(c).Height(historyRowHeight).Padding(0, insetX).AlignItems(ui.Center).FillWidth()
	row.Children(func() {
		if lay.shown[colGraph] {
			ui.Box(c).Width(gw).Height(historyRowHeight).Clip().Opacity(lay.fade(colGraph)).
				Role(ui.RoleImage).Label(describeRow(r, hv.historyMerged(i))).
				Draw(func(p *ui.Painter, rect ui.Rect) {
					// Read again here: the edges built above are reused by
					// the next row.
					var lr lanes.Row
					lr, hv.buf = snap.Row(i, cols, hv.buf)
					graph.Paint(p, rect, lr, graph.DefaultStyle, th.Background, &hv.path)
				})
		}
		ui.Row(c).Gap(spaceM).AlignItems(ui.Center).Grow(1).Shrink(1).MarginStart(lay.gap(colGraph)).Children(func() {
			labels := hv.labels[string(id)]
			for j, l := range labels {
				if j == 3 {
					ui.Text(c, "+"+strconv.Itoa(len(labels)-3)).FontSize(textCaption).TextColor(th.TextMuted)
					break
				}
				refChip(c, l)
			}
			if !ok {
				ui.Text(c, "…").TextColor(th.TextMuted).Grow(1)
				return
			}
			subj := ui.Text(c, subject).SingleLine().Grow(1)
			if ch := hv.loaded(i); ch != nil && ch.Odd(i) {
				subj.TextColor(th.Warning).Description(locales.HistoryBadEncoding())
			}
		})
		if !ok {
			return
		}
		cell := func(k histColumn, e ui.Element) {
			e.SingleLine().Width(lay.w[k]).MarginStart(lay.gap(k)).Opacity(lay.fade(k)).TextColor(th.TextMuted)
		}
		if lay.shown[colAuthor] {
			cell(colAuthor, ui.Text(c, author))
		}
		if lay.shown[colDate] {
			cell(colDate, ui.Text(c, when.Format("2006-01-02 15:04")))
		}
		if lay.shown[colHash] {
			cell(colHash, ui.Text(c, hex.EncodeToString(id)[:8]).Font("monospace").FontSize(textSmall))
		}
	})
	row.ContextMenu(func(m *ui.Menu) {
		ids, n := a.historyIDs(t, h, i)
		a.commitMenu(m, t, id, merge, ids, n)
	})
	if t.s.Queue != nil {
		if _, over := ui.DragOver[draggedCommits](row); over {
			row.Background(th.Accent.Alpha(0.16))
		}
		if v, ok := ui.Drop[draggedCommits](row); ok {
			a.dropOnCommit(t, h, hex.EncodeToString(id), v)
		}
	}
}

// graphWidth fits the graph column to the lanes the history uses.
func graphWidth(snap *lanes.Snapshot) float32 {
	return min(maxGraphWidth, float32(max(1, snap.Width()))*graph.DefaultStyle.LaneWidth)
}

// describeRow is what a graph row reads as: a merge names the commits it
// merges once its text is loaded.
func describeRow(r lanes.Row, merged string) string {
	what := locales.HistoryGraphCommit()
	switch {
	case merged != "":
		what = locales.HistoryGraphMergeOf(merged)
	case r.Merges > 0:
		what = locales.HistoryGraphMergeParents(r.Merges + 1)
	}
	if r.Hidden > 0 {
		what = locales.HistoryGraphHidden(what, r.Hidden)
	}
	return what
}

func refChip(c *ui.Context, l refLabel) {
	th := c.Theme()
	col := th.Accent
	switch l.kind {
	case labelRemote:
		col = th.TextMuted
	case labelTag:
		col = th.Warning
	case labelHead:
		col = th.Danger
	case labelStash, labelBisectGood:
		col = th.Success
	case labelBisectBad:
		col = th.Danger
	case labelBisectSkip:
		col = th.TextMuted
	}
	chip := ui.Text(c, l.name()).FontSize(textCaption).SingleLine().MaxWidth(160).Padding(1, spaceS).Radius(4).Description(l.kind.word())
	switch l.kind {
	case labelTag:
		// Outlined, so a tag is told from a branch of the same name by
		// more than its colour.
		chip.Border(1, col)
	case labelCurrent:
		chip.Bold().Background(col.Alpha(0.16))
	default:
		chip.Background(col.Alpha(0.16))
	}
}

// word names what a label is, for screen readers.
func (k labelKind) word() string {
	switch k {
	case labelCurrent:
		return locales.HistoryKindCurrentBranch()
	case labelRemote:
		return locales.HistoryKindRemoteBranch()
	case labelTag:
		return locales.HistoryKindTag()
	case labelStash:
		return locales.HistoryKindStash()
	case labelBisectBad, labelBisectGood, labelBisectSkip:
		return locales.HistoryKindBisectMark()
	case labelHead:
		return locales.HistoryKindDetachedHead()
	}
	return locales.HistoryKindBranch()
}

// commitMenu is a commit's context menu: copying its hash, message or
// patch, and what acts on it or, when several are picked, on them all.
func (a *App) commitMenu(m *ui.Menu, t *tab, id []byte, merge bool, ids func() []string, n int) {
	if n > 1 {
		a.commitOps(m, t, ids, n)
		a.rewriteMany(m, t, ids, n)
		return
	}
	if m.Item(locales.CommonCopyHash()).Chosen() {
		a.clip = hex.EncodeToString(id)
	}
	if m.Item(locales.HistoryCopyMessage()).Chosen() {
		t.s.LoadCommit(id, func(d session.CommitDetail) {
			switch {
			case d.Err != nil:
				a.notice = errText(d.Err)
			case d.Done:
				a.clip = string(d.Commit.Message)
			}
		})
	}
	if m.Item(locales.HistoryCopyPatch()).Disabled(merge).Chosen() {
		a.copyPatch(t, id)
	}
	m.Separator()
	a.commitActions(m, t, id)
}

func (a *App) copyPatch(t *tab, id []byte) {
	t.s.FormatPatch(id, func(text string, err error) {
		if err != nil {
			a.notice = errText(err)
			return
		}
		a.clip = text
	})
}
