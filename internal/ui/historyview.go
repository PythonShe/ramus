package ui

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/history"
	"ramus/internal/git/lanes"
	"ramus/internal/git/refs"
	"ramus/internal/session"
	"ramus/internal/ui/graph"
)

const (
	historyRowHeight = 24
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
	textFor  *refs.List
	text     map[int]*history.TextChunk
	used     map[int]uint64
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
	// writing is set once a commit-graph write was asked for here.
	writing  bool
	jump     string
	jumpErr  string
	buf      []lanes.Edge
	path     ui.Path
	detail   commitView
	detailW  float32
	chosenID []byte
}

func newHistoryState() historyState {
	return historyState{chosen: -1, detailW: 640, detail: commitView{split: 360}, text: map[int]*history.TextChunk{}, used: map[int]uint64{}, asked: map[int]bool{}}
}

type labelKind uint8

const (
	labelBranch labelKind = iota
	labelCurrent
	labelRemote
	labelTag
	labelHead
	labelStash
)

type refLabel struct {
	text string
	kind labelKind
}

// historyView is the history list beside the chosen commit.
func (a *App) historyView(c *ui.Context, t *tab) {
	th := c.Theme()
	hv := &t.hist
	t.s.LoadHistory()
	h := t.s.History
	hv.frame++
	if hv.textFor != h.For {
		if hv.textFor != nil && hv.chosenID != nil {
			hv.want, hv.wantFor = hv.chosenID, nil
		}
		hv.textFor, hv.chosen, hv.writing = h.For, -1, false
		clear(hv.text)
		clear(hv.used)
		clear(hv.asked)
	}
	a.findWanted(t, h)
	hv.buildLabels(t.s.State, h)
	ui.Split(c, &hv.detailW, func() {
		ui.Column(c).Fill().Children(func() {
			a.historyBar(c, t, h)
			switch {
			case h.Snap == nil && h.Err == nil:
				ui.Column(c).Fill().Center().Children(func() { ui.Spinner(c) })
				return
			case h.Snap == nil || h.Snap.Len() == 0:
				msg := "No commits yet"
				if h.Err != nil {
					msg = errText(h.Err)
				}
				ui.Column(c).Fill().Center().Children(func() {
					ui.Text(c, msg).TextColor(th.TextMuted).Selectable()
				})
				return
			}
			a.historyList(c, t, h)
		})
	}, func() { a.commitDetail(c, t) }).SplitReset(640).Fill()
	if hv.chosen >= 0 && h.Snap != nil && hv.chosen < h.Snap.Len() {
		id := h.OID(h.Snap.Commit(hv.chosen))
		if string(id) != string(hv.chosenID) {
			hv.chosenID = id
		}
	}
}

func (a *App) historyBar(c *ui.Context, t *tab, h session.HistoryState) {
	th := c.Theme()
	hv := &t.hist
	ui.Row(c).Padding(6, 8).Gap(8).AlignItems(ui.Center).Children(func() {
		in := ui.TextInput(c, &hv.jump).Placeholder("Go to hash or ref").Label("Go to hash or ref").Grow(1)
		if in.Submitted() && strings.TrimSpace(hv.jump) != "" {
			a.jumpTo(t, strings.TrimSpace(hv.jump))
		}
		if hv.jumpErr != "" {
			ui.Text(c, hv.jumpErr).FontSize(12).TextColor(th.Danger).SingleLine()
		}
		ui.Text(c, historyStatus(h)).FontSize(12).TextColor(th.TextMuted).SingleLine()
	})
	if h.Advice != nil && h.Done {
		ui.Row(c).Padding(4, 8).Gap(8).AlignItems(ui.Center).Background(th.Surface).Children(func() {
			ui.Text(c, h.Advice.Reason).FontSize(12).Grow(1)
			if ui.Button(c, "Write Commit-Graph").Disabled(hv.writing).Clicked() {
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
	n := strconv.Itoa(h.Snap.Len())
	if !h.Done {
		return n + " commits so far"
	}
	if h.Snap.Len() == 1 {
		return "1 commit"
	}
	return n + " commits"
}

// jumpTo chooses the commit rev names: in the list when history reaches
// it, and in the detail either way.
func (a *App) jumpTo(t *tab, rev string) {
	hv := &t.hist
	hv.jumpErr = ""
	t.s.ResolveCommit(rev, func(id []byte, err error) {
		if err != nil {
			hv.jumpErr = errText(err)
			return
		}
		a.chooseCommit(t, id)
	})
}

// chooseCommit shows commit id in the detail and chooses its row once it
// is found.
func (a *App) chooseCommit(t *tab, id []byte) {
	hv := &t.hist
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
		if t.s.History.For != hv.textFor {
			return
		}
		hv.want, hv.chosen, hv.chosenID = nil, row, want
		hv.list.ScrollTo(row, ui.Center)
	})
}

// buildLabels maps commits to the refs that point at them, when the refs
// change.
func (hv *historyState) buildLabels(st session.State, h session.HistoryState) {
	if st.Refs == hv.labelsFor && h.For == hv.labelsHist && hv.labels != nil {
		return
	}
	hv.labelsFor, hv.labelsHist = st.Refs, h.For
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
			}
		}
	}
	for i, s := range h.Stashes {
		add(s.Base, refLabel{fmt.Sprintf("stash@{%d}", i), labelStash})
	}
	hv.labels = labels
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
			For := h.For
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

// evict drops the chunks used longest ago past the text budget.
func (hv *historyState) evict() {
	total := 0
	for _, c := range hv.text {
		total += c.Bytes()
	}
	for total > historyTextBudget && len(hv.text) > 1 {
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
	hv.list.Key = func(i int) any { return snap.Commit(i) }
	hv.list.Label = func(i int) string {
		s, au, w, ok := a.historyRow(t, h, i)
		if !ok {
			return "commit, loading"
		}
		return s + ", " + au + ", " + w.Format("2006-01-02 15:04")
	}
	ui.List(c, &hv.list, snap.Len(), func(i int) { a.historyRowView(c, t, h, i) }).Grow(1)
}

func (a *App) historyRowView(c *ui.Context, t *tab, h session.HistoryState, i int) {
	th := c.Theme()
	hv := &t.hist
	snap := h.Snap
	subject, author, when, ok := a.historyRow(t, h, i)
	id := h.OID(snap.Commit(i))
	gw := graphWidth(snap)
	cols := graph.DefaultStyle.Columns(gw)
	r, buf := snap.Row(i, cols, hv.buf)
	hv.buf = buf
	merge := r.Merges > 0
	row := ui.Row(c).Height(historyRowHeight).Gap(8).Padding(0, 8).AlignItems(ui.Center).FillWidth()
	row.Children(func() {
		ui.Box(c).Width(gw).Height(historyRowHeight).
			Role(ui.RoleImage).Label(describeRow(r)).
			Draw(func(p *ui.Painter, rect ui.Rect) {
				// Read again here: the edges built above are reused by
				// the next row.
				var lr lanes.Row
				lr, hv.buf = snap.Row(i, cols, hv.buf)
				graph.Paint(p, rect, lr, graph.DefaultStyle, th.Background, &hv.path)
			})
		labels := hv.labels[string(id)]
		for j, l := range labels {
			if j == 3 {
				ui.Text(c, "+"+strconv.Itoa(len(labels)-3)).FontSize(11).TextColor(th.TextMuted)
				break
			}
			refChip(c, l)
		}
		if !ok {
			ui.Text(c, "…").TextColor(th.TextMuted).Grow(1)
			return
		}
		ui.Text(c, subject).SingleLine().Grow(1)
		ui.Text(c, author).SingleLine().Width(120).TextColor(th.TextMuted)
		ui.Text(c, when.Format("2006-01-02 15:04")).SingleLine().Width(112).TextColor(th.TextMuted)
		ui.Text(c, hex.EncodeToString(id)[:8]).Font("monospace").FontSize(12).Width(64).TextColor(th.TextMuted)
	})
	row.ContextMenu(func(m *ui.Menu) { a.commitMenu(m, t, id, merge) })
}

// graphWidth fits the graph column to the lanes the history uses.
func graphWidth(snap *lanes.Snapshot) float32 {
	return min(maxGraphWidth, float32(max(1, snap.Width()))*graph.DefaultStyle.LaneWidth)
}

func describeRow(r lanes.Row) string {
	if r.Merges > 0 {
		return fmt.Sprintf("merge of %d parents", r.Merges+1)
	}
	return "commit"
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
	case labelStash:
		col = th.Success
	}
	chip := ui.Text(c, l.text).FontSize(11).SingleLine().MaxWidth(160).Padding(1, 6).
		Background(col.Alpha(0.16)).Radius(4)
	if l.kind == labelCurrent {
		chip.Bold()
	}
}

// commitMenu is a commit's context menu: copying its hash, message or
// patch.
func (a *App) commitMenu(m *ui.Menu, t *tab, id []byte, merge bool) {
	if m.Item("Copy Hash").Chosen() {
		a.clip = hex.EncodeToString(id)
	}
	if m.Item("Copy Message").Chosen() {
		t.s.LoadCommit(id, func(d session.CommitDetail) {
			if d.Err != nil {
				a.notice = errText(d.Err)
				return
			}
			a.clip = string(d.Commit.Message)
		})
	}
	if m.Item("Copy Patch").Disabled(merge).Chosen() {
		a.copyPatch(t, id)
	}
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
