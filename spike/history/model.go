package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/catfile"
	"ramus/internal/git/commitgraph"
	"ramus/internal/git/gitexe"
	"ramus/internal/git/history"
	"ramus/internal/git/lanes"
	"ramus/internal/git/run"
	"ramus/internal/ui/graph"
	"ramus/spike/internal/measure"
)

const (
	rowHeight  = 24
	graphWidth = 240
	// textBudget bounds the row text kept for scrolled-past rows.
	textBudget = 8 << 20
)

type model struct {
	gitDir string
	update func(func())
	quit   func()

	// Set on the main thread through update.
	err      error
	g        *commitgraph.Graph
	snap     *lanes.Snapshot
	done     bool
	loadTime time.Duration
	first    time.Duration // time to the first screen of rows

	list    ui.ListState
	text    map[int]*history.TextChunk
	used    map[int]uint64
	asked   map[int]bool
	frameNo uint64
	fetch   chan fetchReq

	buf  []lanes.Edge
	path ui.Path

	auto, frames int
	target       int
	last         time.Time
	intervals    []time.Duration
}

type fetchReq struct {
	chunk int
	snap  *lanes.Snapshot
}

func newModel(gitDir string, auto, frames int) *model {
	return &model{
		gitDir: gitDir, auto: auto, frames: frames,
		text: map[int]*history.TextChunk{}, used: map[int]uint64{}, asked: map[int]bool{},
		fetch: make(chan fetchReq, 64),
	}
}

// start loads the history in the background, publishing the layout as it
// grows.
func (m *model) start(update func(func())) {
	m.update = update
	go m.load()
}

func (m *model) load() {
	t0 := time.Now()
	g, err := commitgraph.Open(filepath.Join(m.gitDir, "objects"))
	if err != nil {
		m.update(func() { m.err = err })
		return
	}
	tips, err := refTips(m.gitDir, g)
	if err != nil {
		m.update(func() { m.err = err })
		return
	}
	go m.fetcher(g)
	w := history.NewTopoWalk(g, tips)
	var l lanes.Layout
	next := 1000
	for {
		pos, ps, ok := w.Next()
		if !ok {
			break
		}
		l.Append(pos, ps)
		if l.Len() == next {
			s, el := l.Snapshot(), time.Since(t0)
			first := next == 1000
			m.update(func() {
				m.g, m.snap = g, s
				if first {
					m.first = el
				}
			})
			next *= 4
		}
	}
	s, el := l.Snapshot(), time.Since(t0)
	m.update(func() {
		m.g, m.snap, m.done, m.loadTime = g, s, true, el
		m.err = w.Err()
	})
}

// fetcher loads row text a chunk at a time through one cat-file process.
func (m *model) fetcher(g *commitgraph.Graph) {
	git, err := gitexe.Find(context.Background(), os.Getenv("PATH"))
	if err != nil {
		m.update(func() { m.err = err })
		return
	}
	p, err := catfile.Start(context.Background(), run.New(git, os.Environ(), 8), m.gitDir)
	if err != nil {
		m.update(func() { m.err = err })
		return
	}
	defer p.Close()
	for req := range m.fetch {
		first := req.chunk * history.TextChunkRows
		n := min(history.TextChunkRows, req.snap.Len()-first)
		c, err := history.LoadText(p, g.OID, first, n, req.snap.Commit)
		m.update(func() {
			if err != nil {
				m.err = err
				return
			}
			m.text[req.chunk] = c
			m.evict()
		})
	}
}

func (m *model) evict() {
	total := 0
	for _, c := range m.text {
		total += c.Bytes()
	}
	for total > textBudget {
		oldest, at := -1, ^uint64(0)
		for k := range m.text {
			if m.used[k] < at {
				oldest, at = k, m.used[k]
			}
		}
		total -= m.text[oldest].Bytes()
		delete(m.text, oldest)
		delete(m.asked, oldest)
		delete(m.used, oldest)
	}
}

// refTips lists the commits refs point at. The spike asks git; the engine
// gets its own ref reader later.
func refTips(gitDir string, g *commitgraph.Graph) ([]uint32, error) {
	out, err := exec.Command("git", "--git-dir="+gitDir, "for-each-ref", "--format=%(objectname) %(*objectname)").Output()
	if err != nil {
		return nil, fmt.Errorf("for-each-ref: %w", err)
	}
	var tips []uint32
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(line)
		id, _ := hex.DecodeString(f[len(f)-1])
		if p := g.Lookup(id); p != commitgraph.None {
			tips = append(tips, p)
		}
	}
	return tips, nil
}

// row returns the text of row i, asking for its chunk when it is not
// loaded.
func (m *model) row(i int) (subject, author string, when time.Time, ok bool) {
	k := i / history.TextChunkRows
	c := m.text[k]
	if c == nil {
		if !m.asked[k] {
			select {
			case m.fetch <- fetchReq{chunk: k, snap: m.snap}:
				m.asked[k] = true
			default: // full: asked again next frame
			}
		}
		return "", "", time.Time{}, false
	}
	m.used[k] = m.frameNo
	s, a, w := c.Row(i)
	return s, a, w, true
}

func (m *model) view(c *ui.Context) {
	m.frameNo++
	th := c.Theme()
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(8, 12).Gap(16).Children(func() {
			ui.Text(c, m.status()).TextColor(th.TextMuted)
		})
		if m.snap == nil {
			return
		}
		m.list.Label = func(i int) string { s, _, _, _ := m.row(i); return s }
		ui.List(c, &m.list, m.snap.Len(), func(i int) { m.rowView(c, i) }).Grow(1)
	})
	m.measure(c)
}

func (m *model) rowView(c *ui.Context, i int) {
	th := c.Theme()
	subject, author, when, ok := m.row(i)
	ui.Row(c).Height(rowHeight).Gap(8).Padding(0, 8).Children(func() {
		cols := graph.DefaultStyle.Columns(graphWidth)
		r, buf := m.snap.Row(i, cols, m.buf)
		m.buf = buf
		snap := m.snap
		ui.Box(c).Width(graphWidth).Height(rowHeight).
			Role(ui.RoleImage).Label(describe(r)).
			Draw(func(p *ui.Painter, rect ui.Rect) {
				// Read again here: the edges of the row built above are
				// reused by the next one.
				var row lanes.Row
				row, m.buf = snap.Row(i, cols, m.buf)
				graph.Paint(p, rect, row, graph.DefaultStyle, th.Background, &m.path)
			})
		if !ok {
			ui.Text(c, "…").TextColor(th.TextMuted).Grow(1)
			return
		}
		ui.Text(c, subject).SingleLine().Grow(1)
		ui.Text(c, author).SingleLine().Width(160).TextColor(th.TextMuted)
		ui.Text(c, when.Format("2006-01-02 15:04")).Width(120).TextColor(th.TextMuted)
		ui.Text(c, hex.EncodeToString(m.g.OID(m.snap.Commit(i))[:4])).Font("monospace").Width(70).TextColor(th.TextMuted)
	})
}

func describe(r lanes.Row) string {
	if r.Merges > 0 {
		return fmt.Sprintf("merge of %d parents", r.Merges+1)
	}
	return "commit"
}

func (m *model) status() string {
	switch {
	case m.err != nil:
		return "error: " + m.err.Error()
	case m.snap == nil:
		return "loading…"
	case !m.done:
		return fmt.Sprintf("%d rows so far (first screen after %v)", m.snap.Len(), m.first.Round(time.Millisecond))
	}
	return fmt.Sprintf("%d rows, %d columns, laid out in %v (first screen after %v)",
		m.snap.Len(), m.snap.Width(), m.loadTime.Round(time.Millisecond), m.first.Round(time.Millisecond))
}

// measure scrolls the list by m.auto rows a frame and records the time
// between frames.
func (m *model) measure(c *ui.Context) {
	if m.auto == 0 || !m.done {
		return
	}
	now := time.Now()
	if !m.last.IsZero() {
		m.intervals = append(m.intervals, now.Sub(m.last))
	}
	m.last = now
	if len(m.intervals) >= m.frames {
		report(os.Stdout, m.intervals)
		m.auto = 0
		if m.quit != nil {
			m.quit()
		}
		return
	}
	m.target += m.auto
	m.list.ScrollTo(m.target, ui.Start)
	c.AnimationFrame()
}

func report(w *os.File, ds []time.Duration) {
	measure.Summarize(ds).Print(w, "frame interval")
}
