package ui

import (
	"encoding/hex"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/history"
	"ramus/internal/git/lanes"
	"ramus/internal/git/redact"
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
	// historyFollow is as many rows as the list looks around for a row's
	// key; it holds on to the place and the choice past it by hand.
	historyFollow = 1000
)

// historyState is a tab's history list and the commit chosen in it.
type historyState struct {
	list   ui.ListState
	chosen int
	// loadFor is the load of history shown, snapFor its snapshot.
	loadFor histLoad
	snapFor *lanes.Snapshot
	// text is the commits' text, by object id; textLoad the
	// Session.History.Load it was read after.
	text     historyText
	textLoad int
	// textBudget, when set, replaces historyTextBudget.
	textBudget int
	// asked are the chunks of rows of snapFor whose text was asked for.
	asked    map[int]bool
	inflight int
	frame    uint64

	// labels are built for the refs and the history (its stashes and
	// tags) they were built from.
	labelsFor, labelsHist *refs.List
	labels                map[string][]refLabel

	// want is a commit to choose once it is found in the list: the one
	// chosen before history reloaded, or one jumped to.
	want    []byte
	wantFor *lanes.Snapshot
	jump    string
	jumpErr string
	// jumpN numbers the go-to requests; only the latest is answered.
	jumpN int
	buf   []lanes.Edge
	// picked are the commits chosen together, by object id.
	picked  ui.Selection[string]
	path    ui.Path
	detail  commitView
	detailW float32
	// rowW is the list's width as last laid out, 0 before, when its pane
	// was rowPane wide; pane is its pane's width in this pass, and
	// lastPane in the pass before, which rowW was measured in. colRoom is
	// the width the columns were last laid out for.
	rowW, rowPane, pane, lastPane, colRoom float32
	// cols are the columns as laid out in this frame; colDrag a divider
	// being dragged.
	cols    histLayout
	colDrag colDrag
	// msgWant is the room the message would like: its widest subject
	// shown since history loaded, with its labels, up to
	// historyMessageWant.
	msgWant  float32
	widthKey listWidthKey
	chosenID []byte
	// keptTop is the commit at the top of the list as last laid out.
	keptTop []byte
	filter  historyFilter
}

// listWidthKey is what sets the width of the history list: the window's,
// the sidebar's and the commit's beside it.
type listWidthKey struct {
	window, side, sideOut float32
	noSidebar             bool
	split, listW          float32
	patch                 bool
}

// histLoad names one load of history: the refs, the view and the reload
// it was for.
type histLoad struct {
	refs       *refs.List
	view, load int
}

func loadOf(h session.HistoryState) histLoad { return histLoad{h.For, h.View, h.Load} }

func newHistoryState() historyState {
	return historyState{chosen: -1, detailW: 640, detail: commitView{}, asked: map[int]bool{}}
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
	// A ref's name is the repository's to choose: a bidi override in it
	// could make one branch read as another.
	return redact.Control(l.text)
}

// historyView is the history list beside the chosen commit.
func (a *App) historyView(c *ui.Context, t *tab) {
	hv := &t.hist
	a.loadRefPrefs(t)
	// Set before the list is built, for Choose to work from the first frame.
	hv.list.Selected, hv.list.Selection = &hv.chosen, &hv.picked
	a.keepInView(t, t.s.History)
	t.s.LoadHistory()
	h := t.s.History
	hv.frame++
	a.followHistory(t, h)
	a.findWanted(t, h)
	a.buildLabels(hv, t.s.State, h)
	hv.lastPane, hv.pane = hv.pane, listPane(hv.detailW, &hv.detail)
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
}

// followHistory keeps the place and the choice on their commits as
// history loads again. Commits laid on top of the history shown move its
// rows down by as many, which the list follows on its own by the rows'
// keys, as far as it looks; history laid out anew comes with the rows of
// the commits kept in view (Session.KeepInView). A list showing its top
// stays there instead, for the commits that land to show; the choice
// still stays with its commit. A load of another view chooses the commit
// chosen once it finds it, as one jumped to.
func (a *App) followHistory(t *tab, h session.HistoryState) {
	hv := &t.hist
	if h.Load != hv.textLoad {
		// Asked to reload: the text is read again as it shows.
		hv.textLoad = h.Load
		hv.text.stale()
		clear(hv.asked)
	}
	prev, prevLoad := hv.snapFor, hv.loadFor
	hv.loadFor = loadOf(h)
	if h.Snap == prev {
		return
	}
	hv.snapFor = h.Snap
	clear(hv.asked)
	if h.Text != nil && h.Names >= hv.text.names {
		// In this frame, before the rows: one landing on top shows with
		// its text, never a frame without it.
		a.gotText(t, hv.text.gen, nil, h.Text, h.Names, nil)
	}
	first, last := hv.list.Visible()
	atTop := last >= first && first == 0
	switch {
	case prev == nil || h.Snap == nil || prevLoad == hv.loadFor:
		// The first rows, or more of the same load.
	case h.Base == prev:
		if h.Added > historyFollow && hv.chosen >= 0 {
			// Past where the list looks for the rows' keys.
			hv.list.Choose(hv.chosen + h.Added)
		}
		switch {
		case atTop:
			hv.list.ScrollTo(0, ui.Start)
		case h.Added > historyFollow && last >= first:
			hv.list.ScrollTo(first+h.Added, ui.Start)
		}
	case h.Found != nil:
		row, ok := h.Found[string(hv.chosenID)]
		if !ok {
			row = -1
		}
		hv.list.Choose(row)
		if atTop {
			hv.list.ScrollTo(0, ui.Start)
		} else if top, ok := h.Found[string(hv.keptTop)]; ok && top >= 0 {
			hv.list.ScrollTo(top, ui.Start)
		}
	default:
		hv.list.Choose(-1)
		if hv.chosenID != nil {
			hv.want, hv.wantFor = hv.chosenID, nil
		}
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
		if loadOf(t.s.History) != hv.loadFor {
			return
		}
		hv.want, hv.chosenID = nil, want
		hv.list.Choose(row)
		// Picked as a click picks it, for the choice to follow the commit
		// as commits land above it: the list keeps the app's choice on its
		// item while the item is picked.
		hv.picked.Clear()
		hv.picked.Add(string(want))
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
	a.goWork(nil, func() {
		labels := labelMap(st, h)
		update(func() {
			if hv.labelsFor == refs && hv.labelsHist == hist {
				hv.labels = labels
			}
		})
	})
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

// historyRow returns the text of row i, asking for the text of its chunk
// of rows when it is not read yet.
func (a *App) historyRow(t *tab, h session.HistoryState, i int) (subject, author string, when time.Time, ok bool) {
	hv := &t.hist
	id := h.OID(h.Snap.Commit(i))
	ch, k, ok := hv.text.lookup(id)
	if !ok {
		a.askText(t, h, i/history.TextChunkRows)
		return "", "", time.Time{}, false
	}
	hv.text.use(id, hv.frame)
	if hv.text.wanted(id) {
		// Stale: shown until read again.
		a.askText(t, h, i/history.TextChunkRows)
	}
	s, au, w := ch.Row(k)
	return s, au, w, true
}

// askText reads the text of the commits of chunk k of h's rows that is
// neither read nor being read. Only these: of a chunk of rows that a
// commit on top moved, it reads the text of that commit alone.
func (a *App) askText(t *tab, h session.HistoryState, k int) {
	hv := &t.hist
	if hv.asked[k] || hv.inflight >= textReads {
		return
	}
	hv.asked[k] = true
	first := k * history.TextChunkRows
	last := min(first+history.TextChunkRows, h.Snap.Len())
	var ids [][]byte
	var buf []byte
	for r := first; r < last; r++ {
		if id := h.OID(h.Snap.Commit(r)); hv.text.wanted(id) {
			if buf == nil {
				buf = make([]byte, 0, (last-r)*len(id))
			}
			// Copied: id may point into the commit-graph's mapping, which
			// goes when nothing holds the graph.
			buf = append(buf, id...)
			ids = append(ids, buf[len(buf)-len(id):])
		}
	}
	if len(ids) == 0 {
		return
	}
	hv.text.ask(ids)
	hv.inflight++
	gen := hv.text.gen
	t.s.HistoryText(ids, func(ch *history.TextChunk, names int, err error) {
		hv.inflight--
		a.gotText(t, gen, ids, ch, names, err)
	})
}

// gotText keeps the text ch of the commits ids, read in gen through the
// mailmap names numbers.
func (a *App) gotText(t *tab, gen int, ids [][]byte, ch *history.TextChunk, names int, err error) {
	hv := &t.hist
	if names > hv.text.names {
		// The mailmap changed: the names read before are stale.
		hv.text.names = names
		hv.text.stale()
		gen = hv.text.gen
		clear(hv.asked)
	}
	if err != nil {
		hv.text.add(gen, ids, nil, hv.frame, 0)
		a.tell(t, errText(err))
		return
	}
	budget := historyTextBudget
	if hv.textBudget > 0 {
		budget = hv.textBudget
	}
	if hv.text.add(gen, ids, ch, hv.frame, budget) {
		// Rows whose text went are asked for again when shown.
		clear(hv.asked)
	}
}

// loaded is the chunk holding the text of row i's commit and its place
// in it, nil until it is read.
func (hv *historyState) loaded(h session.HistoryState, i int) (*history.TextChunk, int) {
	ch, k, ok := hv.text.lookup(h.OID(h.Snap.Commit(i)))
	if !ok {
		return nil, 0
	}
	return ch, k
}

// historyMerged is the abbreviated parents of row i when it is a merge
// and its text is loaded, else "".
func (hv *historyState) historyMerged(h session.HistoryState, i int) string {
	if ch, k := hv.loaded(h, i); ch != nil {
		return ch.Merged(k)
	}
	return ""
}

// rowRefs is how many of the refs at a commit its row shows by name.
const rowRefs = 3

func (a *App) historyList(c *ui.Context, t *tab, h session.HistoryState) {
	hv := &t.hist
	snap := h.Snap
	// Keyed by object id, for the place and the choice to follow their
	// commits from one load to the next.
	hv.list.Key = func(i int) any { return string(h.OID(snap.Commit(i))) }
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
		if m := hv.historyMerged(h, i); m != "" {
			out = locales.HistoryRowMerge(m, s, au, date)
		}
		if labels := hv.labels[string(h.OID(snap.Commit(i)))]; len(labels) > 0 {
			// The refs the row shows, named, as the row draws them: a
			// commit can have tens of thousands, too many to read out or
			// to format on every frame.
			names := make([]string, 0, min(len(labels), rowRefs))
			for _, l := range labels[:min(len(labels), rowRefs)] {
				names = append(names, locales.HistoryRefNamed(l.kind.word(), l.name()))
			}
			refs := strings.Join(names, ", ")
			if len(labels) > rowRefs {
				refs = locales.HistoryRefsMore(refs, len(labels)-rowRefs)
			}
			out = locales.HistoryRowAt(out, refs)
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
	w := l.LayoutBounds().W
	if key != hv.widthKey || w != hv.rowW {
		hv.widthKey = key
		c.AnimationFrame()
	}
	hv.rowW, hv.rowPane = w, hv.lastPane
}

// roomW is the width the list has in this pass: as last laid out, and
// what its pane gained or lost since. Measured alone, a file's patch
// opening beside the commit would show the columns a frame or two at
// the width the list had before, over the edge of the narrower one.
func (hv *historyState) roomW() float32 {
	if hv.rowW == 0 || hv.rowPane == 0 {
		return hv.rowW
	}
	return max(0, hv.rowW+hv.pane-hv.rowPane)
}

// keepInView tells the session the commits to find again in a history
// laid out anew: the one at the top of the list and the one chosen.
func (a *App) keepInView(t *tab, h session.HistoryState) {
	hv := &t.hist
	if h.Snap == nil {
		return
	}
	if first, last := hv.list.Visible(); last >= first && first < h.Snap.Len() {
		if id := h.OID(h.Snap.Commit(first)); string(id) != string(hv.keptTop) {
			hv.keptTop = slices.Clone(id)
		}
	}
	t.s.KeepInView(hv.keptTop, hv.chosenID)
}

func (a *App) historyRowView(c *ui.Context, t *tab, h session.HistoryState, i int) {
	th := c.Theme()
	hv := &t.hist
	snap := h.Snap
	subject, author, when, ok := a.historyRow(t, h, i)
	id := h.OID(snap.Commit(i))
	lay := &hv.cols
	gw := lay.w[colGraph]
	style := lay.graphStyle()
	cols := style.Columns(gw)
	r, buf := snap.Row(i, cols, hv.buf)
	hv.buf = buf
	merge := r.Merges > 0
	// No gap: the one after each column opens and closes with it, as the
	// header's divider; the free room at the end follows.
	row := ui.Row(c).Height(historyRowHeight).Padding(0, insetX).PaddingEnd(insetX + lay.end).AlignItems(ui.Center).FillWidth()
	// gap stands for the divider after column k, shown or kept as the
	// column shut.
	gap := func(k histColumn, e ui.Element) {
		switch {
		case lay.shown[k]:
			e.MarginEnd(lay.gap(k))
		case lay.handle[k]:
			ui.Box(c).Width(lay.gap(k))
		}
	}
	row.Children(func() {
		var g ui.Element
		if lay.shown[colGraph] {
			g = ui.Box(c).Width(gw).Height(historyRowHeight).Clip().Opacity(lay.fade(colGraph)).
				Role(ui.RoleImage).Label(describeRow(r, hv.historyMerged(h, i))).
				Draw(func(p *ui.Painter, rect ui.Rect) {
					// Read again here: the edges built above are reused by
					// the next row.
					var lr lanes.Row
					lr, hv.buf = snap.Row(i, cols, hv.buf)
					graph.Paint(p, rect, lr, style, th.Background, &hv.path)
				})
		}
		gap(colGraph, g)
		ui.Row(c).Gap(spaceM).AlignItems(ui.Center).Grow(1).Shrink(1).MarginEnd(spaceM).Children(func() {
			labels := hv.labels[string(id)]
			for j, l := range labels {
				if j == rowRefs {
					ui.Text(c, "+"+strconv.Itoa(len(labels)-rowRefs)).FontSize(textCaption).TextColor(th.TextMuted)
					break
				}
				refChip(c, l)
			}
			if !ok {
				ui.Text(c, "…").TextColor(th.TextMuted).Grow(1)
				return
			}
			hv.wantMessage(c, subject, labels)
			subj := ui.Text(c, subject).SingleLine().Grow(1)
			if ch, k := hv.loaded(h, i); ch != nil && ch.Odd(k) {
				subj.TextColor(th.Warning).Description(locales.HistoryBadEncoding())
			}
		})
		if !ok {
			return
		}
		for k := colAuthor; k < histColumns; k++ {
			if !lay.shown[k] {
				gap(k, ui.Element{})
				continue
			}
			var e ui.Element
			switch k {
			case colAuthor:
				e = ui.Text(c, author)
			case colDate:
				e = ui.Text(c, when.Format("2006-01-02 15:04"))
			default:
				e = ui.Text(c, hex.EncodeToString(id)[:8]).Font("monospace").FontSize(textSmall)
			}
			e.SingleLine().Width(lay.w[k]).Opacity(lay.fade(k)).TextColor(th.TextMuted)
			gap(k, e)
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

// wantMessage widens the room the message would like to fit a subject
// shown with its labels. It measures the text, never the cell, whose
// width the columns decide: the columns follow it, not it them.
func (hv *historyState) wantMessage(c *ui.Context, subject string, labels []refLabel) {
	if hv.msgWant >= historyMessageWant {
		return
	}
	w, _ := c.MeasureText(0, ui.Span{Text: subject})
	for j, l := range labels {
		if j == 3 {
			break
		}
		lw, _ := c.MeasureText(0, ui.Span{Text: l.name(), Size: textCaption})
		w += min(lw, 160) + 2*spaceS + spaceM
	}
	// In steps, for a few subjects a point apart not to lay out again.
	w = min(historyMessageWant, float32(math.Ceil(float64(w)/spaceM))*spaceM)
	if w > hv.msgWant {
		hv.msgWant = w
		c.AnimationFrame()
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
				a.tell(t, errText(d.Err))
			case d.Done:
				a.clip = copiedMessage(d.Commit.Message)
			}
		})
	}
	if m.Item(locales.HistoryCopyPatch()).Disabled(merge).Chosen() {
		a.copyPatch(t, id)
	}
	m.Separator()
	a.commitActions(m, t, id)
}

// copiedMessage is a commit message as Copy Message puts it on the
// clipboard: its lines and tabs kept, without the escapes and controls
// that a terminal it is pasted into would act on.
func copiedMessage(msg []byte) string { return redact.Control(string(msg)) }

func (a *App) copyPatch(t *tab, id []byte) {
	t.s.FormatPatch(id, func(text string, err error) {
		if err != nil {
			a.tell(t, errText(err))
			return
		}
		a.clip = text
	})
}
