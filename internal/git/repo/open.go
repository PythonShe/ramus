package repo

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"ramus/internal/git/catfile"
	"ramus/internal/git/commitgraph"
	"ramus/internal/git/config"
	"ramus/internal/git/lifetime"
	"ramus/internal/git/refs"
	"ramus/internal/git/run"
	"ramus/internal/git/trust"
	"ramus/internal/git/watch"
)

// Repository is an open repository: where it is, what it is configured
// to do, what the engine reads natively, and the lifetime everything it
// owns ends with. Open and the methods that start processes or read files
// run off the main thread.
type Repository struct {
	Location
	// config is the config as last read; Config returns it.
	config atomic.Pointer[config.Config]
	// Caps is the repository as it was at open. What changes while it is
	// open without opening it again (shallow history, grafts, replace
	// refs, core.commitGraph) Capabilities reads anew.
	Caps Capabilities
	// Runner runs git for this repository: hardened when Restricted.
	Runner *run.Runner
	// Restricted is a repository the user has not trusted: git only reads,
	// with nothing from the repository's own config allowed to run.
	Restricted bool
	life       *lifetime.Lifetime

	capsMu   sync.Mutex
	replaced replaceSeen

	mu  sync.Mutex
	cat *catfile.Process
	// releaseCat forgets cat from the lifetime, closing it: a process
	// that died is waited for when replaced, not when the repository
	// closes.
	releaseCat func() error
	// workWatch is the work tree's watcher, once Watch started it.
	workWatch *watch.Watcher
	graph     *commitgraph.Graph
	tried     bool // the graph was looked for
	// broken is set when the graph looked for could not be read.
	broken bool
	// graphAt is the commit-graph files as they were when graph was
	// opened; git writes a new graph, or a new chain, by renaming.
	graphAt [2]fileStamp
	// graphs are the mappings of every graph opened and not yet let go,
	// with what forgets each from the lifetime.
	graphs map[*commitgraph.Mappings]func() error
	gone   *graveyard
}

// graveyard collects the mappings of graphs nothing reaches any more: the
// garbage collector adds them, CommitGraph closes them.
type graveyard struct {
	mu sync.Mutex
	// found are added since the last CommitGraph, aged since the one
	// before.
	found, aged []*commitgraph.Mappings
}

func (y *graveyard) add(m *commitgraph.Mappings) {
	y.mu.Lock()
	y.found = append(y.found, m)
	y.mu.Unlock()
}

// take returns the mappings found before the last call, and ages those
// found since. A reader may still be using bytes it read from a graph
// just after its last use of the Graph itself; closing only what has
// been gone a whole call leaves it far more than that.
func (y *graveyard) take() []*commitgraph.Mappings {
	y.mu.Lock()
	defer y.mu.Unlock()
	out := y.aged
	y.aged, y.found = y.found, nil
	return out
}

type fileStamp struct {
	size int64
	mod  time.Time
}

func stamp(path string) fileStamp {
	fi, err := os.Stat(path)
	if err != nil {
		return fileStamp{size: -1}
	}
	return fileStamp{fi.Size(), fi.ModTime()}
}

// Open finds the repository at or above path, checks it is safe to read,
// and reads its config. trusted says whether the user trusts a
// repository, by its location; one not trusted opens Restricted.
func Open(ctx context.Context, r *run.Runner, path string, trusted func(Location) bool) (*Repository, error) {
	loc, err := Discover(path)
	if err != nil {
		return nil, err
	}
	if err := checkEmbeddedBare(loc); err != nil {
		return nil, err
	}
	if err := CheckOwnership(ctx, r, loc); err != nil {
		return nil, err
	}
	cfg, err := config.Read(ctx, r, loc.GitDir)
	if err != nil {
		return nil, err
	}
	repo := &Repository{
		Location: loc, Caps: Check(loc, cfg), Runner: r,
		life: lifetime.New(context.Background()),
	}
	repo.config.Store(cfg)
	if !trusted(loc) {
		repo.Restricted = true
		repo.Runner = r.Hardened(trust.Hardening(cfg))
	}
	return repo, nil
}

// Config returns the repository's config as last read; safe from any
// goroutine.
func (r *Repository) Config() *config.Config { return r.config.Load() }

// ReloadConfig reads the config again, after it changed. A repository
// not trusted takes the overrides the new config needs first, so no
// command runs a program it newly names. What the config says of the
// repository's format (Caps) stays as it was at open: changing it takes
// opening the repository again. core.commitGraph and core.useReplaceRefs
// Capabilities reads from the config as it is now.
func (r *Repository) ReloadConfig(ctx context.Context) error {
	cfg, err := config.Read(ctx, r.Runner, r.GitDir)
	if err != nil {
		return err
	}
	if r.Restricted {
		r.Runner.Reharden(trust.Hardening(cfg))
	}
	r.config.Store(cfg)
	return nil
}

// Capabilities returns Caps with what can change while the repository is
// open read again: whether it is shallow, has grafts or replace refs, and
// whether the config turns the commit-graph off. It reads a few files, so
// it runs off the main thread.
func (r *Repository) Capabilities() Capabilities {
	r.capsMu.Lock()
	defer r.capsMu.Unlock()
	c := r.Caps
	c.checkHistory(r.Location, r.Config(), &r.replaced)
	return c
}

// CommitGraphUsable is Capabilities().CommitGraphUsable().
func (r *Repository) CommitGraphUsable() bool { return r.Capabilities().CommitGraphUsable() }

// Life is the repository's lifetime: run its goroutines with Life().Go and
// its git processes under Life().Context().
func (r *Repository) Life() *lifetime.Lifetime { return r.life }

// Close ends the repository's lifetime without waiting; Life().Done()
// tells when everything is released. The UI must have let go of what it
// read from the repository's mappings first.
func (r *Repository) Close() { r.life.Close() }

// Refs returns the ref reader for the repository's format, or nil when
// refs go to git.
func (r *Repository) Refs() refs.Reader {
	switch {
	case !r.Caps.NativeRefs():
		return nil
	case r.Caps.RefStorage == "reftable":
		return refs.NewReftable(r.GitDir, r.CommonDir, r.Caps.HashSize)
	}
	return refs.New(r.GitDir, r.CommonDir, r.Caps.HashSize)
}

// CatFile returns the repository's one cat-file process, starting it the
// first time, or again if it died.
func (r *Repository) CatFile() (*catfile.Process, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cat != nil && r.cat.Alive() {
		return r.cat, nil
	}
	if r.releaseCat != nil {
		r.releaseCat()
		r.cat, r.releaseCat = nil, nil
	}
	p, err := catfile.Start(r.life.Context(), r.Runner, r.GitDir)
	if err != nil {
		return nil, err
	}
	release, err := r.life.Own(p)
	if err != nil {
		return nil, err
	}
	r.cat, r.releaseCat = p, release
	return p, nil
}

// CommitGraph returns the commit-graph, or nil when there is none or git
// would not use it. It is opened again when git has written a new one
// since, sharing the layers of a split chain that git kept. A graph
// returned before stays mapped while anything reaches it (a walk, a
// published history's OID), and is unmapped after; bytes read from it
// are copied by whoever keeps them past their hold on the Graph.
func (r *Repository) CommitGraph() (*commitgraph.Graph, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.gone == nil {
		r.gone, r.graphs = &graveyard{}, map[*commitgraph.Mappings]func() error{}
	}
	for _, m := range r.gone.take() {
		if forget, ok := r.graphs[m]; ok {
			delete(r.graphs, m)
			forget()
		}
	}
	if !r.Capabilities().CommitGraphUsable() || r.Caps.HashSize != 20 {
		// Opened again once git would use it again.
		r.graph, r.tried, r.broken, r.graphAt = nil, false, false, [2]fileStamp{}
		return nil, nil
	}
	info := filepath.Join(r.CommonDir, "objects", "info")
	at := [2]fileStamp{stamp(filepath.Join(info, "commit-graph")), stamp(filepath.Join(info, "commit-graphs", "commit-graph-chain"))}
	if r.tried && at == r.graphAt {
		return r.graph, nil
	}
	g, err := commitgraph.OpenReusing(filepath.Join(r.CommonDir, "objects"), r.graph)
	var bad *commitgraph.FormatError
	if errors.Is(err, commitgraph.ErrNotFound) || errors.As(err, &bad) || errors.Is(err, fs.ErrNotExist) {
		// git warns of a graph it cannot read, or a chain missing a
		// layer, and reads the commits themselves: so does history,
		// rather than fail until the graph is written again.
		r.graph, r.tried, r.broken, r.graphAt = nil, true, err != nil && !errors.Is(err, commitgraph.ErrNotFound), at
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// The lifetime owns the mappings, not the Graph, so that the graph
	// can become unreachable while they stay open until the collector
	// says so, or the repository closes.
	m := g.Mappings()
	forget, err := r.life.Own(m)
	if err != nil {
		return nil, err
	}
	r.graphs[m] = forget
	runtime.AddCleanup(g, r.gone.add, m)
	r.graph, r.tried, r.broken, r.graphAt = g, true, false, at
	return g, nil
}

// CommitGraphBroken reports whether the commit-graph CommitGraph last
// looked for is there but could not be read, so CommitGraph returned
// none: writing it anew repairs it.
func (r *Repository) CommitGraphBroken() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.broken
}

// openGraphs returns how many graphs hold mappings.
func (r *Repository) openGraphs() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.graphs)
}
