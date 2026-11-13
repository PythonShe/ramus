package repo

import (
	"context"
	"errors"
	"path/filepath"
	"sync"

	"ramus/internal/git/catfile"
	"ramus/internal/git/commitgraph"
	"ramus/internal/git/config"
	"ramus/internal/git/lifetime"
	"ramus/internal/git/refs"
	"ramus/internal/git/run"
	"ramus/internal/git/trust"
)

// Repository is an open repository: where it is, what it is configured
// to do, what the engine reads natively, and the lifetime everything it
// owns ends with. Open and the methods that start processes or read files
// run off the main thread.
type Repository struct {
	Location
	Config *config.Config
	Caps   Capabilities
	// Runner runs git for this repository: hardened when Restricted.
	Runner *run.Runner
	// Restricted is a repository the user has not trusted: git only reads,
	// with nothing from the repository's own config allowed to run.
	Restricted bool
	life       *lifetime.Lifetime

	mu    sync.Mutex
	cat   *catfile.Process
	graph *commitgraph.Graph
	tried bool // the graph was looked for
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
		Location: loc, Config: cfg, Caps: Check(loc, cfg), Runner: r,
		life: lifetime.New(context.Background()),
	}
	if !trusted(loc) {
		repo.Restricted = true
		repo.Runner = r.Hardened(trust.Hardening(cfg))
	}
	return repo, nil
}

// Life is the repository's lifetime: run its goroutines with Life().Go and
// its git processes under Life().Context().
func (r *Repository) Life() *lifetime.Lifetime { return r.life }

// Close ends the repository's lifetime without waiting; Life().Done()
// tells when everything is released. The UI must have let go of what it
// read from the repository's mappings first.
func (r *Repository) Close() { r.life.Close() }

// Refs returns the ref reader, or nil when refs go to git.
func (r *Repository) Refs() *refs.Store {
	if !r.Caps.NativeRefs() {
		return nil
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
	p, err := catfile.Start(r.life.Context(), r.Runner, r.GitDir)
	if err != nil {
		return nil, err
	}
	if _, err := r.life.Own(p); err != nil {
		return nil, err
	}
	r.cat = p
	return p, nil
}

// CommitGraph returns the commit-graph, or nil when there is none or git
// would not use it. It is opened once, for the repository's lifetime.
func (r *Repository) CommitGraph() (*commitgraph.Graph, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tried {
		return r.graph, nil
	}
	if !r.Caps.CommitGraphUsable() || r.Caps.HashSize != 20 {
		r.tried = true
		return nil, nil
	}
	g, err := commitgraph.Open(filepath.Join(r.CommonDir, "objects"))
	if errors.Is(err, commitgraph.ErrNotFound) {
		r.tried = true
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := r.life.Own(g); err != nil {
		return nil, err
	}
	r.graph, r.tried = g, true
	return g, nil
}
