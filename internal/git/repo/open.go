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
	// TrustDir is the directory the user's trust in this repository is
	// recorded under; see Open.
	TrustDir string
	life     *lifetime.Lifetime
	// hidden is what HideFolders keeps.
	hidden hiddenFolders

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
	// graphGen counts what CommitGraph published, for an open made
	// without the lock to see that another was published meanwhile.
	graphGen uint64
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
//
// Trust is recorded under the common git directory, so a repository's
// worktrees share it, but only for a work tree git itself made one with
// that directory (see TrustDir). Any other folder is asked about with
// CommonDir set to its own TrustDir: a folder whose .git file or
// commondir names a trusted repository does not take that trust, nor
// run the trusted config's hooks on files of its own.
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
		Location: loc, Caps: Check(loc, cfg), Runner: r.Own(),
		life: lifetime.New(context.Background()),
	}
	repo.config.Store(cfg)
	repo.TrustDir = TrustDir(loc, cfg)
	asked := loc
	asked.CommonDir = repo.TrustDir
	if !trusted(asked) {
		repo.Restricted = true
		repo.Runner = r.Hardened(trust.Hardening(cfg))
	}
	return repo, nil
}

// Added reports a worktree added to a repository that has a work tree
// of its own: that one is the repository, and this a part of it. The
// worktrees of a bare repository, which has none, are each a place to
// open it.
func (r *Repository) Added() bool { return Added(r.Location, r.Config()) }

// Added is Repository.Added of the repository at loc, whose config is
// cfg. Whether a repository is bare is what its config says, as to git:
// neither the name of its directory, which may be .git and bare, nor
// where it is, which may be apart from its work tree, tells.
func Added(loc Location, cfg *config.Config) bool {
	return loc.Linked && cfg != nil && !cfg.Bool("core.bare", false)
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
		r.publishGraph(nil, false, false, [2]fileStamp{})
		r.mu.Unlock()
		return nil, nil
	}
	info := filepath.Join(r.CommonDir, "objects", "info")
	at := [2]fileStamp{stamp(filepath.Join(info, "commit-graph")), stamp(filepath.Join(info, "commit-graphs", "commit-graph-chain"))}
	if r.tried && at == r.graphAt {
		g := r.graph
		r.mu.Unlock()
		return g, nil
	}
	prev, gen := r.graph, r.graphGen
	r.mu.Unlock()

	// Opened without the lock, which every read of the repository's
	// catfile or graph takes: a large graph, or a slow disk, would hold
	// them all up.
	g, err := openGraph(filepath.Join(r.CommonDir, "objects"), prev)

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.graphGen != gen && r.tried && r.graphAt == at {
		// Another call opened the same files meanwhile: what it
		// published is used, so that one graph is mapped, not two.
		if g != nil {
			closeGraph(g)
		}
		return r.graph, nil
	}
	var bad *commitgraph.FormatError
	if errors.Is(err, commitgraph.ErrNotFound) || errors.As(err, &bad) || errors.Is(err, fs.ErrNotExist) {
		// git warns of a graph it cannot read, or a chain missing a
		// layer, and reads the commits themselves: so does history,
		// rather than fail until the graph is written again.
		r.publishGraph(nil, true, err != nil && !errors.Is(err, commitgraph.ErrNotFound), at)
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
		// Closed meanwhile: nothing else would unmap it.
		closeGraph(g)
		return nil, err
	}
	r.graphs[m] = forget
	runtime.AddCleanup(g, r.gone.add, m)
	r.publishGraph(g, true, false, at)
	return g, nil
}

// openGraph and closeGraph are seams for a test to make an open slow and
// see a graph closed.
var (
	openGraph  = commitgraph.OpenReusing
	closeGraph = (*commitgraph.Graph).Close
)

// publishGraph records what CommitGraph found, with r.mu held.
func (r *Repository) publishGraph(g *commitgraph.Graph, tried, broken bool, at [2]fileStamp) {
	r.graph, r.tried, r.broken, r.graphAt = g, tried, broken, at
	r.graphGen++
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
