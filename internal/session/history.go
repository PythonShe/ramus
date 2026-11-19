package session

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ramus/internal/git/catfile"
	"ramus/internal/git/commitgraph"
	"ramus/internal/git/commits"
	"ramus/internal/git/diff"
	"ramus/internal/git/history"
	"ramus/internal/git/lanes"
	"ramus/internal/git/mailmap"
	"ramus/internal/git/object"
	"ramus/internal/git/refs"
	"ramus/internal/git/repo"
	"ramus/internal/git/run"
	"ramus/internal/git/signature"
	"ramus/internal/git/textenc"
	"ramus/locales"
)

// HistoryState is the history list as loaded so far; it is published as
// it grows, so the first screen shows before the whole history is read.
// Loaded again as refs move, it is published once whole, the history
// before shown until then: commits added on top come as Added rows over
// the rows of Base, which stay as they were.
type HistoryState struct {
	Snap *lanes.Snapshot
	// OID returns a row's commit object id from its number in Snap; it is
	// safe to call from any goroutine. The bytes may point into the
	// repository's commit-graph mapping, which goes once nothing holds
	// the graph: a caller keeping one past its hold on this state copies
	// it.
	OID  func(id uint32) []byte
	Done bool
	Err  error
	// Native is history ordered from the commit-graph; otherwise git
	// ordered it.
	Native bool
	// Advice is the commit-graph write that would make history faster,
	// which the session runs on its own (see autoGraph).
	Advice *history.Advice
	// First is how long the first screen took; Took the whole history.
	First, Took time.Duration
	// For is the refs it was loaded for, and View the HistoryView, by
	// the number SetHistoryView gave it.
	For  *refs.List
	View int
	// Load numbers the commit-graph changes and the reloads asked for
	// that it was loaded after: with For and View, which load it is.
	Load int
	// Base, when set, is the history published before, and this one is
	// it with Added rows on top: Base's rows are this one's from Added
	// on, the same commits drawn the same.
	Base  *lanes.Snapshot
	Added int
	// Found are the rows of the commits the view keeps in place
	// (KeepInView), by object id, in a history laid out anew; -1 for one
	// it does not have.
	Found map[string]int
	// Filtered is set when a filter picked the commits; until Done, git
	// is still searching.
	Filtered bool
	// TagCommits maps a loose annotated tag's object id to the commit it
	// names, for tags whose ref has no peeled value.
	TagCommits map[string][]byte
	// Stashes are the stash entries, newest first.
	Stashes []Stash
	// Text is the text of the rows a history published again comes with
	// at its top, by Names' mailmap: the commits laid on top, or the
	// first rows of one laid out anew, for a view showing its top to
	// show them with their text from the first frame. Nil on a first
	// load, and when it could not be read.
	Text  *history.TextChunk
	Names int
}

// key is which load h is.
func (h HistoryState) key() histKey { return histKey{h.For, h.View, h.Load} }

// Stash is one stash entry; Base is the commit it was made on.
type Stash struct {
	OID, Base []byte
	Message   string
}

// HistoryView is what history shows: the refs it starts from and the
// commits it keeps.
type HistoryView struct {
	Filter history.Filter
	// Hidden are refs left out, by full name. Only, when not empty, are
	// the only refs shown; HEAD shows when its branch is among them.
	Hidden, Only []string
}

// shows reports whether history starts from the ref named name.
func (v HistoryView) shows(name string) bool {
	if slices.Contains(v.Hidden, name) {
		return false
	}
	return len(v.Only) == 0 || slices.Contains(v.Only, name)
}

// showsHead reports whether history starts from HEAD.
func (v HistoryView) showsHead(head refs.Head) bool {
	if head.Detached || head.Target == "" {
		return len(v.Only) == 0
	}
	return v.shows(head.Target)
}

// hist is the loading of history behind the main thread's HistoryState.
type hist struct {
	mu      sync.Mutex
	cancel  context.CancelFunc
	loading histKey
	// gen numbers the loads; only the latest publishes.
	gen int
	// done is closed once the load started last has ended: the next one
	// waits for it, as it may grow what that one leaves.
	done chan struct{}
	// last is what the last load to end whole left, for the next to lay
	// new commits on top of; the loads, one at a time, read and replace
	// it.
	last *grown
	// keep are the commits the view keeps in place (KeepInView).
	keep [][]byte
	// below is set while the view shows rows below the top of history
	// (ShowsTop): a load then leaves the top's text to be read as it
	// shows, as the view may never show it.
	below atomic.Bool
	// walks counts the loads that ordered the whole history.
	walks int
}

// histKey names a load of history: the refs, the HistoryView by its
// number, and the commit-graph changes and reloads before it, by their
// count.
type histKey struct {
	refs       *refs.List
	view, load int
}

// grown is a history loaded whole, kept so that commits landing on top
// of it are laid out alone rather than all of it again.
type grown struct {
	repo *repo.Repository
	key  histKey
	src  *history.Source
	// in holds the commits of the history, by their numbers in src.
	in     bitset
	starts []uint32
	snap   *lanes.Snapshot
	native bool
}

// bitset is a set of commit numbers, a bit each.
type bitset []uint64

func (b *bitset) add(id uint32) {
	for int(id/64) >= len(*b) {
		*b = append(*b, 0)
	}
	(*b)[id/64] |= 1 << (id % 64)
}

func (b bitset) has(id uint32) bool {
	return int(id/64) < len(b) && b[id/64]&(1<<(id%64)) != 0
}

// SetHistoryView changes what history shows, and loads it again.
func (s *Session) SetHistoryView(v HistoryView) {
	s.historyView = v
	s.historyViewN++
}

// HistoryView returns what history shows.
func (s *Session) HistoryView() HistoryView { return s.historyView }

// KeepInView names the commits the view keeps where they are, the one at
// the top of the list and the one chosen: a history laid out anew finds
// their rows (HistoryState.Found). Main thread only.
func (s *Session) KeepInView(ids ...[]byte) {
	s.hist.mu.Lock()
	defer s.hist.mu.Unlock()
	// Compared as kept, without the nil ids, or a view with nothing
	// chosen would copy them all again in each frame.
	same, k := true, 0
	for _, id := range ids {
		if id == nil {
			continue
		}
		if k >= len(s.hist.keep) || !bytes.Equal(id, s.hist.keep[k]) {
			same = false
			break
		}
		k++
	}
	if same && k == len(s.hist.keep) {
		return
	}
	s.hist.keep = s.hist.keep[:0]
	for _, id := range ids {
		if id != nil {
			s.hist.keep = append(s.hist.keep, slices.Clone(id))
		}
	}
}

// ShowsTop tells whether the view shows the top of history, or a slide
// to it. Main thread only.
func (s *Session) ShowsTop(top bool) { s.hist.below.Store(!top) }

func (h *hist) current(gen int) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.gen == gen
}

// reset forgets the loads of a repository closed. A load of it may have
// handed its rows to the main thread already: the new generation makes
// them stale there.
func (h *hist) reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cancel != nil {
		h.cancel()
	}
	h.gen++
	h.cancel, h.loading, h.last, h.done = nil, histKey{}, nil, nil
}

// filterPublish is how often a search publishes what it found.
const filterPublish = 250 * time.Millisecond

// staleRead bounds the commits read from their objects to order history
// past a commit-graph that lacks them; past it git orders history, which
// it does as fast.
const staleRead = 1 << 17

// growMax bounds the commits laid out on top of a history: past it, the
// whole is laid out anew.
const growMax = 1 << 14

// firstScreen is the rows published first; later publishes come at four
// times the rows before, so a long history is published a dozen times.
const firstScreen = 1000

// LoadHistory starts reading history for the current refs, unless it is
// loaded or loading for them already. A load for older refs is cancelled;
// its rows stay shown until the new ones come: the new first screen for
// another view, or the whole history for the same one.
func (s *Session) LoadHistory() {
	r, list := s.Repo, s.State.Refs
	key := histKey{list, s.historyViewN, s.historyLoadN}
	if r == nil || list == nil || s.History.key() == key {
		return
	}
	s.hist.mu.Lock()
	defer s.hist.mu.Unlock()
	if s.hist.loading == key {
		return
	}
	if s.hist.cancel != nil {
		s.hist.cancel()
	}
	ctx, cancel := context.WithCancel(r.Life().Context())
	s.hist.gen++
	prev, done := s.hist.done, make(chan struct{})
	s.hist.cancel, s.hist.loading, s.hist.done = cancel, key, done
	h := s.History
	whole := h.Done && h.Err == nil && h.Snap != nil && h.View == key.view
	head, gen, view := s.State.Head, s.hist.gen, s.historyView
	err := r.Life().Go(func(context.Context) {
		defer close(done)
		defer cancel()
		if prev != nil {
			select {
			case <-prev:
			case <-ctx.Done():
				// done closing tells the next load that none runs: not
				// before the one before has ended, which every load does.
				<-prev
				return
			}
		}
		s.loadHistory(ctx, gen, r, key, head, view, whole)
	})
	if err != nil {
		close(done)
		cancel()
	}
}

// loadHistory reads history for key. whole publishes it only once whole:
// the same view is shown already, which stays until then.
func (s *Session) loadHistory(ctx context.Context, gen int, r *repo.Repository, key histKey, head refs.Head, view HistoryView, whole bool) {
	t0 := time.Now()
	var tagCommits map[string][]byte
	var stashes []Stash
	publish := func(h HistoryState) {
		if ctx.Err() != nil {
			return
		}
		if whole && !h.Done {
			return
		}
		if whole && h.Base == nil {
			h.Found = s.findKept(ctx, h)
		}
		if (whole || h.Base != nil) && h.Done && h.Err == nil && !s.hist.below.Load() {
			h.Text, h.Names = s.topText(ctx, r, h)
		}
		// The load's context ends when it returns, before the main thread
		// runs this; a newer load, or r closed, is what makes it stale. The
		// rows of a closed repository may point into its commit-graph's
		// mapping, gone once it has closed.
		h.TagCommits, h.Stashes = tagCommits, stashes
		s.update(func() {
			if s.Repo == r && s.hist.current(gen) {
				h.For, h.View, h.Load, h.Filtered = key.refs, key.view, key.load, !view.Filter.IsZero()
				if s.History.View == key.view && s.History.First > 0 && h.First == 0 {
					h.First = s.History.First
				}
				s.History = h
				if h.Done && h.Err == nil {
					s.writeGraphQuietly(h.Advice)
				}
			}
		})
	}
	fail := func(err error) {
		publish(HistoryState{Err: err, Done: true, Snap: (&lanes.Layout{}).Snapshot(), OID: func(uint32) []byte { return nil }})
	}
	cat, err := r.CatFile()
	if err != nil {
		fail(err)
		return
	}
	tips, tc, err := historyTips(cat, key.refs, head, view)
	if err != nil {
		fail(err)
		return
	}
	tagCommits, stashes = tc, readStashes(ctx, r, key.refs)
	if view.Filter.IsZero() && s.growHistory(ctx, gen, r, cat, key, tips, publish, t0) {
		return
	}
	if ctx.Err() != nil {
		return
	}
	g, err := r.CommitGraph()
	if err != nil {
		fail(err)
		return
	}
	// The commit-graph orders history natively, with the commits made
	// since it was written read from their objects. Without one, or with
	// more of them than git would take to order, git orders it.
	var src *history.Source
	var ids []uint32
	covered := false
	if g != nil && g.HasGenerations() {
		src = history.NewSource(g, nil, r.Caps.HashSize)
		ids, err = src.Resolve(ctx, cat, tips, staleRead)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			// Past the limit, or a commit that cannot be read: git
			// orders it, or says what is wrong.
			src = nil
		default:
			// A tag may name a tree or a blob (the kernel's v2.6.11
			// names a tree), which no graph holds; only a commit read
			// means the graph is behind.
			covered = src.Read() == 0
		}
	}
	advice := graphAdvice(r, g, covered)
	var l lanes.Layout
	next := firstScreen
	var first time.Duration
	grew := func(h HistoryState) {
		if l.Len() < next {
			return
		}
		if first == 0 {
			first = time.Since(t0)
		}
		next *= 4
		h.Snap, h.First, h.Advice = l.Snapshot(), first, advice
		publish(h)
	}
	if !view.Filter.IsZero() {
		// A search may find a match a minute: what it found shows at
		// once, then every so often, whatever the count.
		store := commits.New(r.Caps.HashSize)
		var last time.Time
		next = 1
		err = history.Filtered(ctx, r.Runner, r.GitDir, tips, view.Filter, store, func(id uint32, parents []uint32) error {
			l.Append(id, parents)
			if l.Len() >= next || time.Since(last) >= filterPublish {
				if first == 0 {
					first = time.Since(t0)
				}
				last, next = time.Now(), 4*l.Len()
				publish(HistoryState{Snap: l.Snapshot(), OID: store.Snapshot().OID, First: first})
			}
			return nil
		})
		if ctx.Err() != nil {
			return
		}
		if first == 0 {
			first = time.Since(t0)
		}
		snap := store.Snapshot()
		publish(HistoryState{Snap: l.Snapshot(), OID: snap.OID, Done: true, Err: err, First: first, Took: time.Since(t0)})
		return
	}
	var in bitset
	if src != nil {
		oid := src.OIDs()
		w := history.NewTopoWalk(src, ids)
		for n := 0; ; n++ {
			p, ps, ok := w.Next()
			if !ok {
				break
			}
			l.Append(p, ps)
			in.add(p)
			if n&4095 == 0 && ctx.Err() != nil {
				return
			}
			grew(HistoryState{OID: oid, Native: true})
		}
		if first == 0 {
			first = time.Since(t0)
		}
		snap := l.Snapshot()
		if w.Err() == nil {
			s.keepGrown(gen, &grown{repo: r, key: key, src: src, in: in, starts: w.Starts(), snap: snap, native: true})
		}
		publish(HistoryState{Snap: snap, OID: oid, Native: true, Done: true, Err: w.Err(), Advice: advice, First: first, Took: time.Since(t0)})
		return
	}
	store := commits.New(r.Caps.HashSize)
	err = history.RevList(ctx, r.Runner, r.GitDir, tips, store, func(id uint32, parents []uint32) error {
		l.Append(id, parents)
		if l.Len() >= next {
			snap := store.Snapshot()
			grew(HistoryState{OID: snap.OID})
		}
		return nil
	})
	if ctx.Err() != nil {
		return
	}
	if first == 0 {
		first = time.Since(t0)
	}
	snap := l.Snapshot()
	if err == nil && growsWithGit(r) {
		s.keepRevList(gen, r, key, store, tips, snap)
	}
	publish(HistoryState{Snap: snap, OID: store.Snapshot().OID, Done: true, Err: err, Advice: advice, First: first, Took: time.Since(t0)})
}

// graphAdvice is the commit-graph write that would make history of r
// faster, nil when none would or r is not trusted to write.
func graphAdvice(r *repo.Repository, g *commitgraph.Graph, covered bool) *history.Advice {
	if g == nil && !r.Restricted && r.CommitGraphUsable() && r.CommitGraphBroken() {
		// A layer added on top of a graph that cannot be read, or of a
		// chain missing one, would not be read either.
		return &history.Advice{Replace: true, Cmd: history.GraphWrite(r.GitDir, true)}
	}
	if a, ok := history.GraphAdvice(r.GitDir, r.CommitGraphUsable(), g, covered); ok && !r.Restricted {
		return &a
	}
	return nil
}

// growsWithGit reports whether commits landing on a history git ordered
// can be read from their objects on top of it: not when grafts, replace
// refs or a shallow boundary give commits parents other than theirs.
func growsWithGit(r *repo.Repository) bool {
	c := r.Capabilities()
	return !c.Shallow && !c.Grafts && !c.ReplaceRefs
}

// keepGrown keeps a history loaded whole by load gen for the next load to
// grow, unless a newer load or a repository closed made it stale: it
// would keep the closed repository's graph reachable, or replace what
// the newer one kept.
func (s *Session) keepGrown(gen int, g *grown) {
	s.hist.mu.Lock()
	defer s.hist.mu.Unlock()
	s.hist.walks++
	if s.hist.gen == gen {
		s.hist.last = g
	}
}

// keepRevList keeps a history git ordered for the next load to grow:
// store holds every commit of it.
func (s *Session) keepRevList(gen int, r *repo.Repository, key histKey, store *commits.Store, tips [][]byte, snap *lanes.Snapshot) {
	src := history.NewSource(nil, store, r.Caps.HashSize)
	var ids []uint32
	for _, t := range tips {
		if id, ok := store.Lookup(t); ok && store.Added(id) {
			ids = append(ids, id)
		}
	}
	starts, err := history.RevStarts(src, ids)
	if err != nil {
		return
	}
	var in bitset
	for id := range uint32(store.Len()) {
		in.add(id)
	}
	s.keepGrown(gen, &grown{repo: r, key: key, src: src, in: in, starts: starts, snap: snap})
}

// growHistory lays out the commits tips add on top of the history last
// loaded whole, when the rest of it stays as it was, and publishes it. It
// reports false when the whole is to be laid out anew.
func (s *Session) growHistory(ctx context.Context, gen int, r *repo.Repository, cat *catfile.Process, key histKey, tips [][]byte, publish func(HistoryState), t0 time.Time) bool {
	s.hist.mu.Lock()
	last := s.hist.last
	s.hist.mu.Unlock()
	if last == nil || last.repo != r || last.key.view != key.view || last.key.load != key.load {
		return false
	}
	real, err := r.CommitGraph()
	g := real
	if err != nil || g != nil && !g.HasGenerations() {
		g = nil
	}
	if g != last.src.Graph() || !last.native && (g != nil || !growsWithGit(r)) {
		return false
	}
	src := last.src
	ids, err := src.Resolve(ctx, cat, tips, growMax)
	if err != nil {
		return false
	}
	rows, starts, ok, err := history.Above(src, last.in.has, last.starts, ids, growMax)
	if err != nil || !ok {
		return false
	}
	var l lanes.Layout
	var ps []uint32
	for _, id := range rows {
		if ps, err = src.Parents(id, ps[:0]); err != nil {
			return false
		}
		l.Append(id, ps)
	}
	snap, ok := l.Above(last.snap)
	if !ok {
		return false
	}
	// A copy: last stays whole as it was for a load after this one, should
	// this one be stale and not keep what it grew.
	in := slices.Clone(last.in)
	for _, id := range rows {
		in.add(id)
	}
	s.hist.mu.Lock()
	if s.hist.gen == gen {
		s.hist.last = &grown{repo: r, key: key, src: src, in: in, starts: starts, snap: snap, native: last.native}
	}
	s.hist.mu.Unlock()
	advice := graphAdvice(r, real, last.native && src.Read() == 0)
	took := time.Since(t0)
	publish(HistoryState{Snap: snap, OID: src.OIDs(), Native: last.native, Done: true, Advice: advice,
		Base: last.snap, Added: len(rows), First: took, Took: took})
	return true
}

// topText reads the text of the rows at the top of h that a view showing
// its top may not have read: the rows laid on top, or the first chunk of
// a history laid out anew.
func (s *Session) topText(ctx context.Context, r *repo.Repository, h HistoryState) (*history.TextChunk, int) {
	n := min(h.Snap.Len(), history.TextChunkRows)
	if h.Base != nil {
		n = min(n, h.Added)
	}
	if n == 0 {
		return nil, 0
	}
	cat, err := r.CatFile()
	if err != nil {
		return nil, 0
	}
	ids := make([][]byte, n)
	for i := range ids {
		ids[i] = h.OID(h.Snap.Commit(i))
	}
	mm, names := s.names(ctx, r)
	c, err := history.LoadText(cat, ids, mm)
	if err != nil {
		return nil, 0
	}
	return c, names
}

// findKept finds the rows of the commits the view keeps in place in h.
func (s *Session) findKept(ctx context.Context, h HistoryState) map[string]int {
	s.hist.mu.Lock()
	keep := slices.Clone(s.hist.keep)
	s.hist.mu.Unlock()
	if len(keep) == 0 || h.Snap == nil {
		return nil
	}
	found := map[string]int{}
	for _, k := range keep {
		found[string(k)] = -1
	}
	left := len(found)
	for i := 0; i < h.Snap.Len() && left > 0; i++ {
		if i&65535 == 0 && ctx.Err() != nil {
			return nil
		}
		if row, ok := found[string(h.OID(h.Snap.Commit(i)))]; ok && row < 0 {
			found[string(h.OID(h.Snap.Commit(i)))] = i
			left--
		}
	}
	return found
}

// historyTips are the commits history starts from: HEAD, and the
// branches, remote branches and tags, annotated tags peeled. Ramus's own
// refs (snapshots, backups) and the stash are left out.
func historyTips(cat *catfile.Process, list *refs.List, head refs.Head, view HistoryView) ([][]byte, map[string][]byte, error) {
	var tips, tags [][]byte
	// from is the tag ref's own object id each tag read stands for.
	var from [][]byte
	tagCommits := map[string][]byte{}
	seen := map[string]bool{}
	add := func(id []byte) {
		if len(id) > 0 && !seen[string(id)] {
			seen[string(id)] = true
			tips = append(tips, id)
		}
	}
	if view.showsHead(head) {
		add(head.OID)
	}
	for i := range list.Len() {
		name := list.Name(i)
		if !view.shows(name) {
			continue
		}
		switch {
		case strings.HasPrefix(name, "refs/heads/"), strings.HasPrefix(name, "refs/remotes/"):
			if list.Symbolic(i) == "" {
				add(list.OID(i))
			}
		case strings.HasPrefix(name, "refs/tags/"):
			if p := list.Peeled(i); p != nil {
				add(p)
			} else if list.PeelKnown(i) {
				add(list.OID(i)) // a lightweight tag
			} else {
				tags = append(tags, list.OID(i))
				from = append(from, list.OID(i))
			}
		}
	}
	// A tag without a peeled value (a loose one) may be annotated: read it
	// to find the commit, through tags of tags.
	for depth := 0; len(tags) > 0 && depth < 8; depth++ {
		var next, nextFrom [][]byte
		err := cat.Read(tags, func(i int, o catfile.Object) error {
			switch o.Type {
			case "commit":
				add(tags[i])
				if depth > 0 {
					tagCommits[string(from[i])] = tags[i]
				}
			case "tag":
				if t := tagTarget(o.Data); t != nil {
					next, nextFrom = append(next, t), append(nextFrom, from[i])
				}
			}
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
		tags, from = next, nextFrom
	}
	return tips, tagCommits, nil
}

// maxStashes bounds the stash entries read for labels.
const maxStashes = 1000

// readStashes lists the stash entries from the stash's reflog; none when
// there is no stash or it cannot be read, as labels are not worth an
// error.
func readStashes(ctx context.Context, r *repo.Repository, list *refs.List) []Stash {
	has := false
	for i := range list.Len() {
		if list.Name(i) == "refs/stash" {
			has = true
			break
		}
	}
	if !has {
		return nil
	}
	res, err := r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, Read: true,
		Args: []string{"log", "-g", "--no-abbrev", "--max-count=" + strconv.Itoa(maxStashes), "--format=%H%x00%P%x00%gs", "refs/stash", "--"}})
	if err != nil {
		return nil
	}
	var out []Stash
	for _, line := range strings.Split(strings.TrimRight(string(res.Stdout), "\n"), "\n") {
		f := strings.SplitN(line, "\x00", 3)
		if len(f) != 3 {
			continue
		}
		id, err := hex.DecodeString(f[0])
		parents := strings.Fields(f[1])
		if err != nil || len(parents) == 0 {
			continue
		}
		base, err := hex.DecodeString(parents[0])
		if err != nil {
			continue
		}
		out = append(out, Stash{OID: id, Base: base, Message: f[2]})
	}
	return out
}

// commitTips keeps the tips that are commits: those in g, and those
// cat-file says are.
func commitTips(cat *catfile.Process, g *commitgraph.Graph, tips [][]byte) ([][]byte, error) {
	var keep, check [][]byte
	for _, t := range tips {
		if g.Lookup(t) != commitgraph.None {
			keep = append(keep, t)
		} else {
			check = append(check, t)
		}
	}
	err := cat.Read(check, func(i int, o catfile.Object) error {
		if o.Type == "commit" {
			keep = append(keep, check[i])
		}
		return nil
	})
	return keep, err
}

// tagTarget reads the "object" header of a tag.
func tagTarget(data []byte) []byte {
	line, _, _ := bytes.Cut(data, []byte{'\n'})
	h, ok := bytes.CutPrefix(line, []byte("object "))
	if !ok {
		return nil
	}
	id, err := hex.DecodeString(string(h))
	if err != nil {
		return nil
	}
	return id
}

// HistoryText reads the text of the commits ids, at most
// history.TextChunkRows of them, through cat-file, off the main thread,
// and hands it to done on it with the number of the mailmap that named
// their authors: text named through a mailmap of a lower number is stale.
// The ids must stay as they are until then: not the bytes of a
// commit-graph's mapping. Text read of a repository closed meanwhile, as
// Trust opens it again, is not handed over: done gets no text and no
// error, as the history it was read for is gone too.
func (s *Session) HistoryText(ids [][]byte, done func(c *history.TextChunk, names int, err error)) {
	r := s.Repo
	if r == nil {
		done(nil, 0, nil)
		return
	}
	r.Life().Go(func(ctx context.Context) {
		cat, err := r.CatFile()
		var c *history.TextChunk
		var names int
		if err == nil {
			var mm *mailmap.Map
			mm, names = s.names(ctx, r)
			c, err = history.LoadText(cat, ids, mm)
		}
		s.handOver(r, func() { done(c, names, err) }, func() { done(nil, 0, nil) })
	})
}

// CommitDetail is one commit as the detail view shows it.
type CommitDetail struct {
	OID     []byte
	Commit  object.Commit
	Parents [][]byte
	// Files are the files changed from the first parent, renames found;
	// nil while they are read.
	Files *diff.Raw
	// Lines are the lines each of Files adds and deletes, in its order;
	// nil while they are counted, and when git could not count them.
	// Sum adds them up.
	Lines []diff.Lines
	Sum   diff.Lines
	// Sig is git's verdict on a signed commit's signature, its Code 0
	// while not checked: in a repository not trusted, the verifier named
	// in its config does not run. SigErr is why git gave none.
	Sig    signature.Status
	SigErr error
	// Odd is set when the message or a name was not valid in the commit's
	// encoding (UTF-8 when it names none): its bytes show as \xNN.
	Odd bool
	// Checking is set while git checks the signature.
	Checking bool
	// Renames is git leaving renames unfound, too many files changed:
	// they show deleted and added. Compare sets it.
	Renames diff.RenameLimit
	// Names is the number of the mailmap that named its people, as
	// HistoryState's Names: a detail named through a lower one is stale.
	Names int
	// WorkTree is set for a comparison with the work tree, from its one
	// parent, as CompareWorkTree reads it: it has no OID.
	WorkTree bool
	// Done is set on the last of the details handed over.
	Done bool
	Err  error
}

// LoadCommit reads one commit off the main thread and hands it to done on
// it as it is read: the message first, then its changed files, then the
// lines they change and its signature, side by side, so a commit of
// 10,000 files or a slow signature check holds back nothing read before. cancel stops the reading; done is not called
// after it. A commit read before is handed over whole, at once. A read
// whose repository closed and opened again (Trust) ends with ErrReopened.
func (s *Session) LoadCommit(oid []byte, done func(CommitDetail)) (cancel func()) {
	r := s.Repo
	if r == nil {
		return func() {}
	}
	key := commitKey(oid)
	// One kept from before the mailmap changed names its people as it no
	// longer does.
	if a, ok := s.answers().Get(key); ok && a.detail.Names >= s.History.Names {
		done(*a.detail)
		return func() {}
	}
	ctx, stop := context.WithCancel(s.viewContext(r))
	// dropped and told are read and written on the main thread only.
	dropped, told := false, false
	reopened := func() {
		if !dropped && !told {
			told = true
			done(CommitDetail{OID: oid, Done: true, Err: ErrReopened})
		}
	}
	deliver := func(d CommitDetail) {
		s.handOver(r, func() {
			if d.Done && d.Err == nil {
				s.keep(r, key, answer{detail: &d})
			}
			if !dropped {
				done(d)
			}
		}, reopened)
	}
	r.Life().Go(func(context.Context) {
		defer stop()
		d := CommitDetail{OID: oid}
		d.Err = s.readCommit(ctx, r, &d, deliver)
		if ctx.Err() != nil && d.Err != nil {
			if closedUnder(r) {
				s.handOver(r, nil, reopened)
			}
			return // cancelled, or the repository closed
		}
		d.Done, d.Checking = true, false
		deliver(d)
	})
	return func() { dropped = true; stop() }
}

// readCommit fills d, handing each part to publish as it is read; the
// last part is left for the caller.
func (s *Session) readCommit(ctx context.Context, r *repo.Repository, d *CommitDetail, publish func(CommitDetail)) error {
	cat, err := r.CatFile()
	if err != nil {
		return err
	}
	var data []byte
	err = cat.Read([][]byte{d.OID}, func(_ int, o catfile.Object) error {
		if o.Type != "commit" {
			return errors.New("session: not a commit")
		}
		data = bytes.Clone(o.Data)
		return nil
	})
	if err != nil {
		return err
	}
	if d.Commit, err = object.ParseCommit(data); err != nil {
		return err
	}
	// Shown and copied as UTF-8, as git log shows them.
	enc, ok := string(d.Commit.Encoding), true
	for _, f := range []*[]byte{&d.Commit.Message, &d.Commit.Author.Name, &d.Commit.Author.Email, &d.Commit.Committer.Name, &d.Commit.Committer.Email} {
		text, valid := textenc.Decode(*f, enc)
		*f, ok = []byte(text), ok && valid
	}
	d.Odd = !ok
	// Each person as the mailmap names them, as git show does.
	mm, names := s.names(ctx, r)
	d.Names = names
	for _, p := range []*object.Signature{&d.Commit.Author, &d.Commit.Committer} {
		name, email := mm.Lookup(string(p.Name), string(p.Email))
		p.Name, p.Email = []byte(name), []byte(email)
	}
	for _, p := range object.ParentIDs(data) {
		if id, err := hex.DecodeString(string(p)); err == nil {
			d.Parents = append(d.Parents, id)
		}
	}
	var parent []byte
	if len(d.Parents) > 0 {
		parent = d.Parents[0]
	}
	publish(*d)
	if d.Files, _, err = diff.Renames(ctx, r.Runner, r.GitDir, r.Caps.HashSize, parent, d.OID); err != nil {
		return err
	}
	publish(*d)
	// Counting the lines reads every file changed, and checking the
	// signature may wait on a key server: they run side by side, each
	// shown when done. mu orders their changes to d and what is handed
	// over of them.
	var mu sync.Mutex
	set := func(f func()) {
		mu.Lock()
		defer mu.Unlock()
		f()
		publish(*d)
	}
	counted := make(chan struct{})
	defer func() { <-counted }()
	from, files := parent, d.Files
	if from == nil {
		from = emptyTree(r.Caps.HashSize)
	}
	go func() {
		defer close(counted)
		defer s.recoverWork()
		ns, err := diff.CommitLines(ctx, r.Runner, r.GitDir, from, d.OID)
		if err != nil || ctx.Err() != nil {
			return
		}
		ls := ns.ForRaw(files)
		set(func() { d.Lines, d.Sum = ls, diff.Total(ls) })
	}()
	if d.Commit.Signed && !r.Restricted {
		set(func() { d.Checking = true })
		sig, sigErr := signature.Commit(ctx, r.Runner, r.GitDir, hex.EncodeToString(d.OID))
		if ctx.Err() != nil {
			return ctx.Err()
		}
		set(func() { d.Sig, d.SigErr, d.Checking = sig, sigErr, false })
	}
	return nil
}

// emptyTree is the tree with nothing in it, the side a root commit's
// patch is taken against.
func emptyTree(hashSize int) []byte {
	h := "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	if hashSize == 32 {
		h = "6ef19b41225c5369f1c104d45d8d85efa9b057b53b14b4b9b939dd74decc5321"
	}
	id, _ := hex.DecodeString(h)
	return id
}

// CommitPatch reads the patch of paths in commit d against its first
// parent off the main thread, leaving out the white space ws says, and
// hands it to done on it, with the whole files sides names. A newer call
// cancels a read still running, whose done is not called. A patch read
// before is handed over at once. A read whose repository closed and
// opened again (Trust) is answered ErrReopened.
func (s *Session) CommitPatch(d CommitDetail, paths []string, maxBytes int, ws diff.Whitespace, sides Sides, done func(PatchResult)) {
	s.commitPatchOf(d, paths, maxBytes, ws, sides, false, done)
}

// HostPatch is CommitPatch with the patch as a pull request's host makes
// it (diff.HostPatch), all white space kept: the lines a comment goes on.
func (s *Session) HostPatch(d CommitDetail, paths []string, maxBytes int, sides Sides, done func(PatchResult)) {
	s.commitPatchOf(d, paths, maxBytes, 0, sides, true, done)
}

func (s *Session) commitPatchOf(d CommitDetail, paths []string, maxBytes int, ws diff.Whitespace, sides Sides, host bool, done func(PatchResult)) {
	r := s.Repo
	if r == nil {
		done(PatchResult{Err: errors.New("session: the repository is not open")})
		return
	}
	parent := emptyTree(r.Caps.HashSize)
	if len(d.Parents) > 0 {
		parent = d.Parents[0]
	}
	s.commitPatch.mu.Lock()
	if s.commitPatch.cancel != nil {
		s.commitPatch.cancel()
	}
	ctx, cancel := context.WithCancel(s.viewContext(r))
	s.commitPatch.cancel = cancel
	s.commitPatch.mu.Unlock()
	key := commitPatchKey(parent, d.OID, paths, maxBytes, ws, sides)
	if host {
		key = "h" + key
	}
	if a, ok := s.answers().Get(key); ok {
		cancel()
		done(*a.patch)
		return
	}
	r.Life().Go(func(context.Context) {
		var pt diff.Patch
		var err error
		if host {
			pt, err = diff.HostPatch(ctx, r.Runner, r.GitDir, parent, d.OID, paths, maxBytes)
		} else {
			pt, err = diff.CommitPatch(ctx, r.Runner, r.GitDir, parent, d.OID, paths, -1, maxBytes, ws)
		}
		var res PatchResult
		if err == nil {
			res.Truncated = pt.Truncated
			res.Parsed, err = diff.Parse(pt.Text)
		}
		if err == nil && !res.Truncated {
			readSides(r, sides, &res)
		}
		res.Err = err
		// A newer read, or the view left, cancels it; r closing too, which
		// is answered.
		reopened := func() {
			if ctx.Err() == nil || closedUnder(r) {
				cancel()
				done(PatchResult{Err: ErrReopened})
			}
		}
		if ctx.Err() != nil {
			if closedUnder(r) {
				s.handOver(r, nil, reopened)
			}
			cancel()
			return
		}
		s.handOver(r, func() {
			live := ctx.Err() == nil
			cancel()
			if err == nil {
				s.keep(r, key, answer{patch: &res})
			}
			if live {
				done(res)
			}
		}, reopened)
	})
}

// ResolveCommit finds the commit a hash, ref or other revision names,
// off the main thread, and hands it to done on it, unless the repository
// closed meanwhile.
func (s *Session) ResolveCommit(rev string, done func([]byte, error)) {
	r := s.Repo
	if r == nil {
		return
	}
	r.Life().Go(func(ctx context.Context) {
		res, err := r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, Read: true, Untranslated: true,
			Args: []string{"rev-parse", "--verify", "--quiet", "--end-of-options", rev + "^{commit}"}})
		var id []byte
		if err == nil {
			id, err = hex.DecodeString(strings.TrimSpace(string(res.Stdout)))
		}
		if err != nil {
			err = errors.New(locales.SessionNoCommitNamed(strconv.Quote(rev)))
		}
		s.handOver(r, func() { done(id, err) }, nil)
	})
}

// FindRow looks for the row of commit oid in h off the main thread, as
// it can take a scan of every row, and hands it, or -1, to done on it,
// unless the repository closed meanwhile.
func (s *Session) FindRow(h HistoryState, oid []byte, done func(int)) {
	r := s.Repo
	if r == nil || h.Snap == nil {
		done(-1)
		return
	}
	r.Life().Go(func(ctx context.Context) {
		row := -1
		for i := range h.Snap.Len() {
			if i&65535 == 0 && ctx.Err() != nil {
				return
			}
			if bytes.Equal(h.OID(h.Snap.Commit(i)), oid) {
				row = i
				break
			}
		}
		s.handOver(r, func() { done(row) }, nil)
	})
}

// MaxFormatPatch bounds a patch copied as text.
const MaxFormatPatch = 64 << 20

// FormatPatch reads commit oid as git format-patch writes it, off the
// main thread, and hands it to done on it, unless the repository closed
// meanwhile.
func (s *Session) FormatPatch(oid []byte, done func(string, error)) {
	r := s.Repo
	if r == nil {
		return
	}
	r.Life().Go(func(ctx context.Context) {
		res, err := r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, Read: true, MaxOutput: MaxFormatPatch,
			Args: []string{"format-patch", "-1", "--stdout", "--no-color", "--no-ext-diff", hex.EncodeToString(oid), "--"}})
		var text string
		if err == nil {
			text = string(res.Stdout)
		}
		s.handOver(r, func() { done(text, err) }, nil)
	})
}
