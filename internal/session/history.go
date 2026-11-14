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
	"time"

	"ramus/internal/git/catfile"
	"ramus/internal/git/commitgraph"
	"ramus/internal/git/commits"
	"ramus/internal/git/diff"
	"ramus/internal/git/history"
	"ramus/internal/git/lanes"
	"ramus/internal/git/object"
	"ramus/internal/git/refs"
	"ramus/internal/git/repo"
	"ramus/internal/git/run"
	"ramus/internal/git/textenc"
	"ramus/internal/git/write"
)

// HistoryState is the history list as loaded so far; it is published as
// it grows, so the first screen shows before the whole history is read.
type HistoryState struct {
	Snap *lanes.Snapshot
	// OID returns a row's commit object id from its number in Snap; it is
	// safe to call from any goroutine.
	OID  func(id uint32) []byte
	Done bool
	Err  error
	// Native is history ordered from the commit-graph; otherwise git
	// ordered it.
	Native bool
	// Advice offers writing a commit-graph when one would help.
	Advice *history.Advice
	// First is how long the first screen took; Took the whole history.
	First, Took time.Duration
	// For is the refs it was loaded for, and View the HistoryView, by
	// the number SetHistoryView gave it.
	For  *refs.List
	View int
	// Filtered is set when a filter picked the commits; until Done, git
	// is still searching.
	Filtered bool
	// TagCommits maps a loose annotated tag's object id to the commit it
	// names, for tags whose ref has no peeled value.
	TagCommits map[string][]byte
	// Stashes are the stash entries, newest first.
	Stashes []Stash
}

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
	loading *refs.List
	view    int
	// gen numbers the loads; only the latest publishes.
	gen int
}

// SetHistoryView changes what history shows, and loads it again.
func (s *Session) SetHistoryView(v HistoryView) {
	s.historyView = v
	s.historyViewN++
}

// HistoryView returns what history shows.
func (s *Session) HistoryView() HistoryView { return s.historyView }

func (h *hist) current(gen int) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.gen == gen
}

// filterPublish is how often a search publishes what it found.
const filterPublish = 250 * time.Millisecond

// firstScreen is the rows published first; later publishes come at four
// times the rows before, so a long history is published a dozen times.
const firstScreen = 1000

// LoadHistory starts reading history for the current refs, unless it is
// loaded or loading for them already. A load for older refs is cancelled;
// its rows stay shown until the new first screen comes.
func (s *Session) LoadHistory() {
	r, list, vn := s.Repo, s.State.Refs, s.historyViewN
	if r == nil || list == nil || s.History.For == list && s.History.View == vn {
		return
	}
	s.hist.mu.Lock()
	defer s.hist.mu.Unlock()
	if s.hist.loading == list && s.hist.view == vn {
		return
	}
	if s.hist.cancel != nil {
		s.hist.cancel()
	}
	ctx, cancel := context.WithCancel(r.Life().Context())
	s.hist.gen++
	s.hist.cancel, s.hist.loading, s.hist.view = cancel, list, vn
	head, gen, view := s.State.Head, s.hist.gen, s.historyView
	r.Life().Go(func(context.Context) {
		defer cancel()
		s.loadHistory(ctx, gen, r, list, head, view, vn)
	})
}

func (s *Session) loadHistory(ctx context.Context, gen int, r *repo.Repository, list *refs.List, head refs.Head, view HistoryView, vn int) {
	t0 := time.Now()
	var tagCommits map[string][]byte
	var stashes []Stash
	publish := func(h HistoryState) {
		if ctx.Err() != nil {
			return
		}
		// The load's context ends when it returns, before the main thread
		// runs this; a newer load is what makes it stale.
		h.TagCommits, h.Stashes = tagCommits, stashes
		s.update(func() {
			if s.hist.current(gen) {
				h.For, h.View, h.Filtered = list, vn, !view.Filter.IsZero()
				if s.History.For == list && s.History.View == vn && s.History.First > 0 && h.First == 0 {
					h.First = s.History.First
				}
				s.History = h
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
	tips, tc, err := historyTips(cat, list, head, view)
	if err != nil {
		fail(err)
		return
	}
	tagCommits, stashes = tc, readStashes(ctx, r, list)
	g, err := r.CommitGraph()
	if err != nil {
		fail(err)
		return
	}
	pos, covered := history.Covered(g, tips)
	if g != nil && !covered {
		// A tag may name a tree or a blob (the kernel's v2.6.11 names a
		// tree), which no graph holds; only a missing commit means the
		// graph is behind.
		if tips, err = commitTips(cat, g, tips); err != nil {
			fail(err)
			return
		}
		pos, covered = history.Covered(g, tips)
	}
	var advice *history.Advice
	if a, ok := history.GraphAdvice(r.GitDir, r.Caps.CommitGraphUsable(), g, covered); ok && !r.Restricted {
		advice = &a
	}
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
	if covered {
		oid := g.OID
		w := history.NewTopoWalk(g, pos)
		for n := 0; ; n++ {
			p, ps, ok := w.Next()
			if !ok {
				break
			}
			l.Append(p, ps)
			if n&4095 == 0 && ctx.Err() != nil {
				return
			}
			grew(HistoryState{OID: oid, Native: true})
		}
		if first == 0 {
			first = time.Since(t0)
		}
		publish(HistoryState{Snap: l.Snapshot(), OID: oid, Native: true, Done: true, Err: w.Err(), Advice: advice, First: first, Took: time.Since(t0)})
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
	snap := store.Snapshot()
	publish(HistoryState{Snap: l.Snapshot(), OID: snap.OID, Done: true, Err: err, Advice: advice, First: first, Took: time.Since(t0)})
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

// HistoryText reads the text of rows of h through cat-file, off the main
// thread, and hands it to done on it.
func (s *Session) HistoryText(h HistoryState, first, n int, done func(*history.TextChunk, error)) {
	r := s.Repo
	if r == nil || h.Snap == nil {
		return
	}
	ids := make([]uint32, n)
	for i := range ids {
		ids[i] = h.Snap.Commit(first + i)
	}
	update := s.update
	r.Life().Go(func(context.Context) {
		cat, err := r.CatFile()
		var c *history.TextChunk
		if err == nil {
			c, err = history.LoadText(cat, h.OID, first, n, func(row int) uint32 { return ids[row-first] })
		}
		update(func() { done(c, err) })
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
	// Signature is git's %G? letter for a signed commit ("G" good, "B"
	// bad, "U" good but untrusted, "E" cannot check...), "" when unsigned
	// or not checked: in a repository not trusted, the verifier named in
	// its config does not run.
	Signature, Signer string
	// Odd is set when the message or a name was not valid in the commit's
	// encoding (UTF-8 when it names none): its bytes show as \xNN.
	Odd bool
	// Checking is set while git checks the signature.
	Checking bool
	// Done is set on the last of the details handed over.
	Done bool
	Err  error
}

// LoadCommit reads one commit off the main thread and hands it to done on
// it as it is read: the message first, then its changed files, then its
// signature, so a commit of 10,000 files or a slow signature check holds
// back nothing read before. cancel stops the reading; done is not called
// after it.
func (s *Session) LoadCommit(oid []byte, done func(CommitDetail)) (cancel func()) {
	r := s.Repo
	if r == nil {
		return func() {}
	}
	ctx, stop := context.WithCancel(r.Life().Context())
	// dropped is read and written on the main thread only.
	dropped := false
	deliver := func(d CommitDetail) {
		s.update(func() {
			if !dropped {
				done(d)
			}
		})
	}
	r.Life().Go(func(context.Context) {
		defer stop()
		d := CommitDetail{OID: oid}
		d.Err = s.readCommit(ctx, r, &d, deliver)
		if ctx.Err() != nil && d.Err != nil {
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
	if d.Commit.Signed && !r.Restricted {
		d.Checking = true
		publish(*d)
		res, err := r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, Read: true,
			Args: []string{"log", "-1", "--no-walk", "--format=%G?%x00%GS", hex.EncodeToString(d.OID), "--"}})
		if err == nil {
			letter, signer, _ := strings.Cut(strings.TrimSpace(string(res.Stdout)), "\x00")
			d.Signature, d.Signer = letter, signer
		}
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
// cancels a read still running, whose done is not called.
func (s *Session) CommitPatch(d CommitDetail, paths []string, maxBytes int, ws diff.Whitespace, sides Sides, done func(PatchResult)) {
	r := s.Repo
	if r == nil {
		done(PatchResult{Err: errors.New("session: the repository is not open")})
		return
	}
	parent := emptyTree(r.Caps.HashSize)
	if len(d.Parents) > 0 {
		parent = d.Parents[0]
	}
	update := s.update
	s.commitPatch.mu.Lock()
	if s.commitPatch.cancel != nil {
		s.commitPatch.cancel()
	}
	ctx, cancel := context.WithCancel(r.Life().Context())
	s.commitPatch.cancel = cancel
	s.commitPatch.mu.Unlock()
	r.Life().Go(func(context.Context) {
		defer cancel()
		pt, err := diff.CommitPatch(ctx, r.Runner, r.GitDir, parent, d.OID, paths, -1, maxBytes, ws)
		var res PatchResult
		if err == nil {
			res.Truncated = pt.Truncated
			res.Parsed, err = diff.Parse(pt.Text)
		}
		if err == nil && !res.Truncated {
			res.Old, res.New, res.HasSides = readSides(r, sides)
		}
		res.Err = err
		if ctx.Err() != nil {
			return
		}
		update(func() { done(res) })
	})
}

// ResolveCommit finds the commit a hash, ref or other revision names,
// off the main thread, and hands it to done on it.
func (s *Session) ResolveCommit(rev string, done func([]byte, error)) {
	r := s.Repo
	if r == nil {
		return
	}
	update := s.update
	r.Life().Go(func(ctx context.Context) {
		res, err := r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, Read: true, Untranslated: true,
			Args: []string{"rev-parse", "--verify", "--quiet", "--end-of-options", rev + "^{commit}"}})
		var id []byte
		if err == nil {
			id, err = hex.DecodeString(strings.TrimSpace(string(res.Stdout)))
		}
		if err != nil {
			err = errors.New("no commit is named " + strconv.Quote(rev))
		}
		update(func() { done(id, err) })
	})
}

// FindRow looks for the row of commit oid in h off the main thread, as
// it can take a scan of every row, and hands it, or -1, to done on it.
func (s *Session) FindRow(h HistoryState, oid []byte, done func(int)) {
	r := s.Repo
	if r == nil || h.Snap == nil {
		done(-1)
		return
	}
	update := s.update
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
		update(func() { done(row) })
	})
}

// MaxFormatPatch bounds a patch copied as text.
const MaxFormatPatch = 64 << 20

// FormatPatch reads commit oid as git format-patch writes it, off the
// main thread, and hands it to done on it.
func (s *Session) FormatPatch(oid []byte, done func(string, error)) {
	r := s.Repo
	if r == nil {
		return
	}
	update := s.update
	r.Life().Go(func(ctx context.Context) {
		res, err := r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, Read: true, MaxOutput: MaxFormatPatch,
			Args: []string{"format-patch", "-1", "--stdout", "--no-color", "--no-ext-diff", hex.EncodeToString(oid), "--"}})
		var text string
		if err == nil {
			text = string(res.Stdout)
		}
		update(func() { done(text, err) })
	})
}

// WriteCommitGraph writes the commit-graph h.Advice offers, then loads
// history again from it; the rows shown stay until the new ones come.
func (s *Session) WriteCommitGraph(a history.Advice) error {
	j, err := s.Submit(write.Op{Name: "Write commit-graph", Cmd: a.Cmd})
	if err != nil {
		return err
	}
	r, update := s.Repo, s.update
	r.Life().Go(func(ctx context.Context) {
		select {
		case <-j.Done():
		case <-ctx.Done():
			return
		}
		update(func() {
			if s.Repo != r {
				return
			}
			s.hist.mu.Lock()
			s.hist.loading = nil
			s.hist.mu.Unlock()
			s.History.For = nil
			s.LoadHistory()
		})
	})
	return nil
}
