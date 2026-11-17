package ui

import (
	"bytes"
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

// historySlide is how the list slides down to the commits that land on
// its top, and how its rows glide to their places as rows go.
var historySlide = ui.Animation{Duration: 200 * time.Millisecond}

// historyState is a tab's history list and the commit chosen in it.
type historyState struct {
	list   ui.ListState
	chosen int
	// shownIDs are the commits whose authors the frame showed, whose
	// pictures the next asks for.
	shownIDs [][]byte
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
	// wantFrom is, for the commit the history chose by itself as it
	// came, one past the first row shown then: the list is scrolled to it
	// only if the user has not scrolled since. 0 scrolls to it anyway.
	wantFrom int
	jump     string
	jumpErr  string
	// find is the go-to field; focusFind asks it for the focus, and
	// focusList the list, once they are built.
	find                 ui.Handle
	focusFind, focusList bool
	// started is set once the history chose its first commit by itself.
	started bool
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
	// inView are the commits of the rows the list showed, first to last,
	// of viewSnap's rows viewFirst to viewLast, in one buffer: a history
	// laid out anew keeps the first of them still in it where it was.
	inView              [][]byte
	inViewBuf           []byte
	viewSnap            *lanes.Snapshot
	viewFirst, viewLast int
	keep                [][]byte
	// scroll slides the list to its top as commits land there: tracked
	// only while it slides, for the list to keep its place by its rows
	// otherwise. slideFrom is the frame the commits landed in, 0 for
	// none: the slide starts in the next, from where the list laid the
	// rows it showed out in it.
	scroll    ui.ScrollState
	slideFrom uint64
	filter    historyFilter
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
	// The pictures of the authors of the rows shown in the last frame.
	a.syncAvatars(t, hv.shownIDs)
	hv.shownIDs = hv.shownIDs[:0]
	a.followHistory(t, h)
	switch {
	case hv.slideFrom == 0:
	case hv.frame > hv.slideFrom:
		hv.scroll.ScrollTo(0, 0, historySlide)
		hv.slideFrom = 0
	default:
		c.AnimationFrame()
	}
	a.startAtHead(t, h)
	a.findWanted(t, h)
	a.buildLabels(hv, t.s.State, h)
	chosen := hv.chosenID != nil
	hv.lastPane, hv.pane = hv.pane, listPane(hv.detailW, &hv.detail, chosen)
	listBesideCommit(c, &hv.detailW, 640, &hv.detail, chosen, &hv.list, func() {
		ui.Column(c).Fill().Children(func() {
			a.bisectBar(c, t)
			a.historyBar(c, t, h)
			switch {
			case h.Snap == nil && h.Err == nil:
				loading(c, "")
				return
			case h.Snap == nil || h.Snap.Len() == 0:
				a.historyEmpty(c, t, h)
				return
			}
			a.historyList(c, t, h)
		})
	}, func() { a.commitDetail(c, t, hv.chosenID, &hv.detail) }).Fill()
	if hv.chosen >= 0 && h.Snap != nil && hv.chosen < h.Snap.Len() {
		id := h.OID(h.Snap.Commit(hv.chosen))
		if string(id) != string(hv.chosenID) {
			// A row chosen by hand outranks a go-to still resolving, and a
			// commit still looked for in the list. A copy: id may point
			// into the commit-graph's mapping, which goes when the
			// repository reopens.
			hv.chosenID = slices.Clone(id)
			hv.jumpN++
			hv.jumpErr = ""
			hv.want, hv.wantFor, hv.wantFrom = nil, nil, 0
		}
	}
}

// historyEmpty says why history shows no commits, with the way out where
// there is one: the filter cleared, or the load tried again.
func (a *App) historyEmpty(c *ui.Context, t *tab, h session.HistoryState) {
	msg, action := locales.HistoryNoCommits(), ""
	var act func()
	switch {
	case h.Err != nil:
		msg, action, act = errText(h.Err), locales.RepoTryAgain(), func() { a.reload(t) }
	case h.Filtered:
		msg, action, act = locales.HistoryNoMatch(), locales.HistoryFilterClearAll(), func() { a.clearHistoryFilter(t) }
	}
	ui.Column(c).Fill().Center().Gap(spaceL).Children(func() {
		ui.Text(c, msg).TextColor(c.Theme().TextMuted).Selectable()
		if act != nil && ui.Button(c, action).Clicked() {
			act()
		}
	})
}

func (a *App) historyBar(c *ui.Context, t *tab, h session.HistoryState) {
	th := c.Theme()
	hv := &t.hist
	ui.Row(c).Padding(barPadY, insetX).MinHeight(barHeight).Gap(spaceM).AlignItems(ui.Center).Children(func() {
		in := ui.SearchField(c, &hv.jump).Placeholder(locales.HistoryGoTo()).Label(locales.HistoryGoTo()).
			Bind(&hv.find).Grow(1).MinWidth(leastFieldW)
		if hv.focusFind {
			hv.focusFind = false
			hv.find.SelectAll(c)
		}
		if in.Changed() {
			// What it said was of the text before.
			hv.jumpErr = ""
		}
		if in.Submitted() && strings.TrimSpace(hv.jump) != "" {
			a.jumpTo(t, strings.TrimSpace(hv.jump))
		}
		if hv.jumpErr != "" {
			ui.Text(c, hv.jumpErr).FontSize(textSmall).TextColor(th.Danger).SingleLine().Shrink(1).MinWidth(0)
		}
		// The count gives way before the field does.
		ui.Row(c).Gap(spaceS).AlignItems(ui.Center).Shrink(1).MinWidth(0).Children(func() {
			status := historyStatus(h)
			if h.Snap != nil && !h.Done {
				ui.Spinner(c).FontSize(textSmall).Label(status)
			}
			ui.Text(c, status).FontSize(textSmall).TextColor(th.TextMuted).SingleLine().Shrink(1).MinWidth(0)
		})
		a.filterButton(c, t)
	})
	a.filterBar(c, t)
}

// focusHistoryFind puts the keyboard focus in history's go-to field, its
// text chosen, once History shows.
func (a *App) focusHistoryFind(t *tab) { t.hist.focusFind = true }

// followHistory keeps the place and the choice on their commits as
// history loads again. Commits laid on top of the history shown move its
// rows down by as many, which the list follows on its own by the rows'
// keys, as far as it looks; history laid out anew comes with the rows of
// the commits kept in view (Session.KeepInView): the first commit shown
// that it still holds stays where it showed, whatever went above it. A
// list showing its top slides down to the commits that land there
// instead, or shows its new top; the choice still stays with its commit.
// A load of another view chooses the commit chosen once it finds it, as
// one jumped to.
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
	// A list on its way to its top, rows that came above it sliding in,
	// shows its top.
	atTop := last >= first && hv.atTop(first)
	shown := last - first + 1
	if hv.scroll.Scrolling() || hv.slideFrom != 0 {
		// A slide on its way stops: the rows moved under it. The next
		// starts from where they are.
		hv.scroll, hv.slideFrom = ui.ScrollState{}, 0
	}
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
			// From where the rows shown went, mid-slide or not.
			hv.slideDown(first+h.Added, first+h.Added, shown)
		case h.Added > historyFollow && last >= first:
			hv.list.ScrollTo(first+h.Added, ui.Start)
		}
	case h.Found != nil:
		row, ok := h.Found[string(hv.chosenID)]
		if !ok {
			row = -1
		}
		hv.list.Choose(row)
		k, at := hv.survivor(h.Found)
		switch {
		case atTop && k >= 0:
			hv.slideDown(at-k, at-k, shown)
		case atTop:
			hv.list.ScrollTo(0, ui.Start)
		case k >= 0:
			hv.list.ScrollTo(max(0, at-k), ui.Start)
		}
	default:
		hv.list.Choose(-1)
		if hv.chosenID != nil {
			hv.want, hv.wantFor = hv.chosenID, nil
		}
	}
}

// atTop reports whether the list, first row first, shows its top or
// slides to it.
func (hv *historyState) atTop(first int) bool {
	return first == 0 || hv.scroll.Scrolling() || hv.slideFrom != 0
}

// slideDown shows the top of the list, which n rows came above: from
// row top, where the rows it showed went, it slides there in the frames
// after this one, which lays them out where they showed. Rows that went
// (n below 0), and more than it shows, it shows at once.
func (hv *historyState) slideDown(n, top, shown int) {
	if n <= 0 || n > shown || top <= 0 {
		hv.list.ScrollTo(0, ui.Start)
		return
	}
	hv.list.ScrollTo(top, ui.Start)
	hv.slideFrom = hv.frame
}

// survivor returns the first commit in view that a history laid out
// anew still holds: how far down the view it was, and its row now; -1
// for none.
func (hv *historyState) survivor(found map[string]int) (k, row int) {
	for k, id := range hv.inView {
		if row, ok := found[string(id)]; ok && row >= 0 {
			return k, row
		}
	}
	return -1, -1
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
		typed := strings.TrimSpace(hv.jump) == rev
		switch {
		case err != nil && typed && !hexish(rev):
			// No commit by that name: words typed here are mostly
			// looked for in the messages.
			hv.jump = ""
			a.findInMessages(t, rev)
		case err != nil:
			hv.jumpErr = errText(err)
		default:
			if typed {
				// Gone to; left in the field, it would read as a search
				// the list stays narrowed by.
				hv.jump = ""
			}
			a.chooseCommit(t, id)
		}
	})
}

// hexish reports whether s looks like an abbreviated hash, which is
// looked up by name alone. Shorter than git's own abbreviations, it is
// as likely a word, as "added" or "decade", searched for in the messages
// once it names no commit.
func hexish(s string) bool {
	if len(s) < 7 {
		return false
	}
	for _, r := range s {
		if !('0' <= r && r <= '9' || 'a' <= r && r <= 'f' || 'A' <= r && r <= 'F') {
			return false
		}
	}
	return true
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
	hv.want, hv.wantFor, hv.wantFrom = id, nil, 0
	hv.chosenID = id
	hv.chosen = -1
}

// startAtHead chooses the checked-out commit as history first comes,
// unless one is chosen already, so that its detail shows rather than an
// empty pane.
func (a *App) startAtHead(t *tab, h session.HistoryState) {
	hv := &t.hist
	if hv.started || h.Snap == nil || h.Snap.Len() == 0 {
		return
	}
	hv.started = true
	if hv.chosenID != nil || hv.want != nil {
		return
	}
	top := h.OID(h.Snap.Commit(0))
	id := t.s.State.Head.OID
	if id != nil && !bytes.Equal(id, top) {
		// Found further down, it is scrolled to, unless the user
		// scrolled the list meanwhile.
		a.showCommit(t, bytes.Clone(id))
		first, _ := hv.list.Visible()
		hv.wantFrom = first + 1
		return
	}
	// At the top, where the list is: chosen as a click chooses it, with
	// nothing to look for, which would scroll the list back there once
	// found. A copy: the id may point into the commit-graph's mapping.
	hv.chosenID = bytes.Clone(top)
	hv.list.Choose(0)
	hv.picked.Clear()
	hv.picked.Add(string(top))
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
		from := hv.wantFrom
		hv.want, hv.wantFrom, hv.chosenID = nil, 0, want
		hv.list.Choose(row)
		// Picked as a click picks it, for the choice to follow the commit
		// as commits land above it: the list keeps the app's choice on its
		// item while the item is picked.
		hv.picked.Clear()
		hv.picked.Add(string(want))
		if first, _ := hv.list.Visible(); from == 0 || first == from-1 {
			hv.list.ScrollTo(row, ui.Center)
		}
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
	// A row shows the first few: the current branch leads, whatever its
	// name, and remote branches, which mostly repeat a local one, go to
	// the count of the rest first.
	for _, l := range labels {
		slices.SortStableFunc(l, func(x, y refLabel) int { return labelRank(x.kind) - labelRank(y.kind) })
	}
	return labels
}

// labelRank orders the labels at a commit as its row shows them.
func labelRank(k labelKind) int {
	switch k {
	case labelHead:
		return 0
	case labelCurrent:
		return 1
	case labelBranch:
		return 2
	case labelTag:
		return 3
	case labelRemote:
		return 4
	}
	return 5
}

// historyRow returns the text of row i, asking for the text of its chunk
// of rows when it is not read yet.
func (a *App) historyRow(t *tab, h session.HistoryState, i int) (subject, author, email string, when time.Time, ok bool) {
	hv := &t.hist
	id := h.OID(h.Snap.Commit(i))
	ch, k, ok := hv.text.lookup(id)
	if !ok {
		a.askText(t, h, i/history.TextChunkRows)
		return "", "", "", time.Time{}, false
	}
	hv.text.use(id, hv.frame)
	if hv.text.wanted(id) {
		// Stale: shown until read again.
		a.askText(t, h, i/history.TextChunkRows)
	}
	// In the viewer's zone, not each author's, so that the times of rows
	// compare.
	s, au, w := ch.Row(k)
	return s, au, ch.Email(k), w.Local(), true
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
	// Rows glide to their places as commits come or go, as a reset or a
	// forced update takes them, rather than jump there.
	hv.list.MoveAnimation = &historySlide
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
		s, au, _, w, ok := a.historyRow(t, h, i)
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
	l := ui.List(c, &hv.list, snap.Len()).Rows(func(r ui.ListRow) { a.historyRowView(c, t, h, r) }).Label(locales.HistoryCommits()).Grow(1)
	if hv.scroll.Scrolling() {
		l.TrackScroll(&hv.scroll)
	}
	// Keys for the chosen commit while the list has the focus: Copy
	// copies its hash, and Option with Up and Down goes to its child and
	// its parent, as the arrows go to the rows above and below.
	hv.list.OnShortcut(c, ui.Cmd, ui.KeyC, func() { a.copyChosen(t, h) })
	hv.list.OnShortcut(c, ui.Alt, ui.KeyDown, func() { a.chooseRelative(t, false) })
	hv.list.OnShortcut(c, ui.Alt, ui.KeyUp, func() { a.chooseRelative(t, true) })
	// Escape lets go of the commits picked with the chosen one; with
	// none, it is left to what else takes it, as a patch open beside.
	if hv.picked.Len() > 1 {
		hv.list.OnShortcut(c, 0, ui.KeyEscape, func() { hv.unpick() })
	}
	if hv.focusList && hv.list.Focus(c) {
		hv.focusList = false
	}
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

// unpick lets go of the commits picked, the chosen one kept.
func (hv *historyState) unpick() {
	hv.picked.Clear()
	if hv.chosenID != nil {
		hv.picked.Add(string(hv.chosenID))
	}
}

// copyChosen copies the hash of the commit chosen, or of every commit
// picked with it, a line each, oldest first, as they would be picked
// onto another branch.
func (a *App) copyChosen(t *tab, h session.HistoryState) {
	hv := &t.hist
	if hv.chosenID == nil {
		return
	}
	if hv.chosen < 0 || h.Snap == nil || hv.chosen >= h.Snap.Len() {
		a.clip = hex.EncodeToString(hv.chosenID)
		return
	}
	all, n := a.historyIDs(t, h, hv.chosen)
	if n > 1 {
		// As the menu's Copy Hashes copies them.
		ids := all()
		slices.Reverse(ids)
		a.clip = strings.Join(ids, "\n") + "\n"
		return
	}
	a.clip = all()[0]
}

// chooseRelative goes to the chosen commit's first child, or its first
// parent, once its detail has read them.
func (a *App) chooseRelative(t *tab, child bool) {
	hv := &t.hist
	cv := &hv.detail
	var to []byte
	switch {
	case !bytes.Equal(cv.id, hv.chosenID):
	case child && cv.rel != nil && len(cv.rel.Children) > 0:
		to = cv.rel.Children[0]
	case !child && cv.d != nil && len(cv.d.Parents) > 0:
		to = cv.d.Parents[0]
	}
	if to != nil {
		a.chooseCommit(t, bytes.Clone(to))
	}
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
// laid out anew: the ones in view and the one chosen.
func (a *App) keepInView(t *tab, h session.HistoryState) {
	hv := &t.hist
	if h.Snap == nil {
		return
	}
	// Only of the rows the list laid out: a history just come is not.
	first, last := hv.list.Visible()
	if h.Snap == hv.snapFor && last >= first && last < h.Snap.Len() &&
		(h.Snap != hv.viewSnap || first != hv.viewFirst || last != hv.viewLast) {
		hv.viewSnap, hv.viewFirst, hv.viewLast = h.Snap, first, last
		// Copied: the ids may point into the commit-graph's mapping,
		// which goes when the repository reopens.
		hv.inViewBuf = hv.inViewBuf[:0]
		for i := first; i <= last; i++ {
			hv.inViewBuf = append(hv.inViewBuf, h.OID(h.Snap.Commit(i))...)
		}
		hv.inView = hv.inView[:0]
		for at, i := 0, first; i <= last; i++ {
			n := len(h.OID(h.Snap.Commit(i)))
			hv.inView = append(hv.inView, hv.inViewBuf[at:at+n:at+n])
			at += n
		}
	}
	hv.keep = append(append(hv.keep[:0], hv.chosenID), hv.inView...)
	t.s.KeepInView(hv.keep...)
	t.s.ShowsTop(last < first || hv.atTop(first))
}

func (a *App) historyRowView(c *ui.Context, t *tab, h session.HistoryState, lr ui.ListRow) {
	th := c.Theme()
	hv := &t.hist
	i, k, chosen := lr.Index, rowInk(lr), lr.Selected()
	// Tooltips only on the row under the pointer, the one that can show
	// them: the others' would be made for nothing on every frame.
	hovered := lr.Hovered()
	snap := h.Snap
	subject, author, email, when, ok := a.historyRow(t, h, i)
	id := h.OID(snap.Commit(i))
	lay := &hv.cols
	gw := lay.w[colGraph]
	style := lay.graphStyle()
	cols := style.Columns(gw)
	r, buf := snap.Row(i, cols, hv.buf)
	hv.buf = buf
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
			gk := graph.Ink{Dark: th.Dark, Cut: th.TextMuted, Chosen: chosen, Solid: th.AccentText}
			g = ui.Box(c).Width(gw).Height(historyRowHeight).Clip().Opacity(lay.fade(colGraph)).
				Role(ui.RoleImage).Label(describeRow(r, hv.historyMerged(h, i))).
				Draw(func(p *ui.Painter, rect ui.Rect) {
					// Read again here: the edges built above are reused by
					// the next row.
					var lr lanes.Row
					lr, hv.buf = snap.Row(i, cols, hv.buf)
					graph.Paint(p, rect, lr, style, gk, &hv.path)
				})
		}
		gap(colGraph, g)
		ui.Row(c).Gap(spaceM).AlignItems(ui.Center).Grow(1).Shrink(1).MarginEnd(spaceM).Children(func() {
			labels := hv.labels[string(id)]
			// The refs give way to the subject, which the row is read by:
			// as many as leave it its least, the rest counted.
			n := rowChips(c, labels, lay.messageW(hv.roomW())-historySubjectMin)
			for _, l := range labels[:n] {
				rowChip(c, l, chosen).Shrink(1)
			}
			if more := len(labels) - n; more > 0 {
				plus := ui.Text(c, "+"+strconv.Itoa(more)).FontSize(textCaption).TextColor(k.Muted).SingleLine()
				if hovered {
					plus.Tooltip(hiddenRefs(labels[n:]))
				}
			}
			if !ok {
				ui.Text(c, "…").TextColor(k.Muted).Grow(1)
				return
			}
			hv.wantMessage(c, subject, labels)
			subj := ui.Text(c, subject).SingleLine().Grow(1).Shrink(1)
			if hovered {
				// The column cuts a long subject short.
				subj.Tooltip(subject)
			}
			if ch, j := hv.loaded(h, i); ch != nil && ch.Odd(j) {
				subj.TextColor(k.Warning).Description(locales.HistoryBadEncoding())
			}
		})
		if !ok {
			return
		}
		for col := colAuthor; col < histColumns; col++ {
			if !lay.shown[col] {
				gap(col, ui.Element{})
				continue
			}
			var e ui.Element
			switch col {
			case colAuthor:
				// A copy: the id may be in the commit-graph's mapping,
				// which can go before the next frame reads it.
				hv.shownIDs = append(hv.shownIDs, bytes.Clone(id))
				e = ui.Row(c).Gap(spaceXS).AlignItems(ui.Center).Clip().Children(func() {
					// The name beside it says who it is.
					a.avatar(c, t, author, email, avatarRow).Role(ui.RoleNone).Label("")
					ui.Text(c, author).SingleLine().Grow(1).Shrink(1).MinWidth(0)
				})
			case colDate:
				e = ui.Text(c, when.Format("2006-01-02 15:04"))
				if hovered {
					e.Tooltip(since(a.Now(), when))
				}
			default:
				e = ui.Text(c, hex.EncodeToString(id)[:8]).Font("monospace").FontSize(textSmall)
			}
			e.SingleLine().Width(lay.w[col]).Opacity(lay.fade(col)).TextColor(k.Muted)
			gap(col, e)
		}
	})
	row.ContextMenu(func(m *ui.Menu) {
		a.commitMenu(m, t, h, i, r.Merges)
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
	for _, l := range labels[:min(len(labels), rowRefs)] {
		w += chipWidth(c, l) + spaceM
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

// refChipMax is the widest a ref's chip grows; a longer name is cut in
// its middle.
const refChipMax = 160

// historySubjectMin is the least of the message's room that a row's refs
// leave its subject.
const historySubjectMin = 120

// chipWidth is how wide refChip draws l.
func chipWidth(c *ui.Context, l refLabel) float32 {
	w, _ := c.MeasureText(0, ui.Span{Text: l.name(), Size: textCaption})
	return min(w, refChipMax) + 2*spaceS
}

// rowChips is how many of labels a row shows by name in room: up to
// rowRefs, as many as fit beside the count of the rest.
func rowChips(c *ui.Context, labels []refLabel, room float32) int {
	for n := min(len(labels), rowRefs); n > 0; n-- {
		var w float32
		for _, l := range labels[:n] {
			w += chipWidth(c, l) + spaceM
		}
		if rest := len(labels) - n; rest > 0 {
			pw, _ := c.MeasureText(0, ui.Span{Text: "+" + strconv.Itoa(rest), Size: textCaption})
			w += pw + spaceM
		}
		if w <= room {
			return n
		}
	}
	return 0
}

// hiddenRefs lists the refs labels name, as the tooltip of the count of
// those a row leaves out: up to refsNamed of them.
func hiddenRefs(labels []refLabel) string {
	const refsNamed = 20
	names := make([]string, 0, min(len(labels), refsNamed))
	for _, l := range labels[:min(len(labels), refsNamed)] {
		names = append(names, l.name())
	}
	out := strings.Join(names, ", ")
	if len(labels) > refsNamed {
		out = locales.HistoryRefsMore(out, len(labels)-refsNamed)
	}
	return out
}

func refChip(c *ui.Context, l refLabel) { rowChip(c, l, false) }

// rowChip is the chip of ref l; on a chosen row, drawn on the accent, in
// AccentText, as the theme's colours there are lost.
func rowChip(c *ui.Context, l refLabel, chosen bool) ui.Element {
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
	fill := col.Alpha(0.16)
	if chosen {
		col, fill = th.AccentText.Alpha(0.7), th.AccentText.Alpha(0.2)
	}
	chip := ui.Text(c, l.name()).FontSize(textCaption).SingleLine().TruncateMiddle().MaxWidth(refChipMax).Padding(1, spaceS).Radius(radiusSmall).
		Description(l.kind.word()).Tooltip(l.name())
	if chosen {
		chip.TextColor(th.AccentText)
	}
	switch l.kind {
	case labelTag:
		// Outlined, so a tag is told from a branch of the same name by
		// more than its colour.
		chip.Border(1, col)
	case labelCurrent:
		chip.Bold().Background(fill)
	default:
		chip.Background(fill)
	}
	return chip
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

// commitMenu is the context menu of history row i, a commit of merges
// parents past the first: copying its hash, message or patch, showing
// its author's commits, and what acts on it or, when several are picked,
// on them all.
func (a *App) commitMenu(m *ui.Menu, t *tab, h session.HistoryState, i, merges int) {
	// A copy: the id may be in the commit-graph's mapping, which can go
	// before the menu's choice is made.
	id := bytes.Clone(h.OID(h.Snap.Commit(i)))
	ids, n := a.historyIDs(t, h, i)
	if n > 1 {
		a.copyMany(m, t, ids, n)
		m.Separator()
		a.pickOps(m, t, ids, n, pickedMerges(t, h))
		a.rewriteMany(m, t, ids, n)
		m.Separator()
		a.commitOps(m, t, ids, n)
		return
	}
	merge := merges > 0
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
	if _, author, email, _, ok := a.historyRow(t, h, i); ok && email != "" {
		m.Separator()
		if m.Item(locales.HistoryShowAuthorCommits(redact.Control(author))).Chosen() {
			a.showPersonHistory(t, redact.Control(author), email)
		}
	}
	m.Separator()
	a.commitItems(m, t, id, merges)
}

// copyMany adds to a menu what copies the hashes and the subjects of
// commits picked (hex, newest first), one a line, oldest first, as
// git log --reverse lists them.
func (a *App) copyMany(m *ui.Menu, t *tab, ids func() []string, n int) {
	if m.Item(locales.HistoryCopyHashes(n)).Chosen() {
		list := ids()
		slices.Reverse(list)
		a.clip = strings.Join(list, "\n") + "\n"
	}
	if m.Item(locales.HistoryCopySubjects(n)).Chosen() {
		var oids [][]byte
		for _, id := range ids() {
			if b, err := hex.DecodeString(id); err == nil {
				oids = append(oids, b)
			}
		}
		slices.Reverse(oids)
		t.s.LoadSubjects(oids, func(subjects []string) {
			if len(subjects) > 0 {
				a.clip = redact.Control(strings.Join(subjects, "\n")) + "\n"
			}
		})
	}
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
