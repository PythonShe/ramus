// Package session is one open repository as the app sees it: opened off
// the main thread, kept fresh from the watcher, with its write queue. It
// knows nothing of the UI: what it learns it hands to update, which runs
// on the main thread, and the UI reads State only there.
package session

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"ramus/internal/git/refs"
	"ramus/internal/git/repo"
	"ramus/internal/git/run"
	"ramus/internal/git/status"
	"ramus/internal/git/trust"
	"ramus/internal/git/write"
)

// State is what the UI shows of a repository. It is replaced, never
// changed in place, so a view may hold one for a frame.
type State struct {
	Opening bool
	// Err is why the repository could not be opened.
	Err error
	// Restricted is a repository opened read-only until the user trusts
	// it.
	Restricted bool
	// Fallbacks name what goes to git, for the status line.
	Fallbacks []string

	Head       refs.Head
	Refs       *refs.List
	Worktrees  []repo.Worktree
	Submodules []repo.Submodule
	Status     *status.Status
	Operation  repo.Operation
	// RefreshErr is the last refresh that failed; the state before it
	// stays shown.
	RefreshErr error
	// Refreshed is when status was last read.
	Refreshed time.Time
}

// Session is one open repository.
type Session struct {
	Path   string
	runner *run.Runner
	trust  *trust.Store
	update func(func())

	// Main thread only.
	State State
	Repo  *repo.Repository
	Queue *write.Queue
	// Log is what the app ran here for the user, oldest first.
	Log []*LogEntry
	// Writes counts the writes that have ended, so a view of something a
	// write may change (snapshots, undo) knows to read it again.
	Writes int
	// History is the history list as loaded; LoadHistory fills it.
	History HistoryState

	mu      sync.Mutex
	dirty   repo.Change
	wake    chan struct{}
	closed  bool
	timeNow func() time.Time
	// stop cancels an open in progress; opened is the repository once
	// open, before the main thread has taken it, so Close reaches both.
	stop   context.CancelFunc
	opened *repo.Repository
	// running counts opens and the repositories they made until each has
	// stopped; done closes after Close once it is zero.
	running sync.WaitGroup
	done    chan struct{}
	patch   patches
	hist    hist
	// historyView is what history shows, numbered by historyViewN.
	historyView  HistoryView
	historyViewN int
	// commitPatch cancels a commit patch read that a newer one replaced.
	commitPatch patches
	stats       statsLoad
	tools       diffTools
	refInfo     refLoad
	blame       blames
}

// refreshSeen is what the last refresh of one repository read, to tell what a change
// notice really changed: an index rewritten with the same content key
// needs no status, and refs read with the same values keep the list
// everything was built from.
type refreshSeen struct {
	index     repo.IndexKey
	indexRead bool
	refs      *refs.List
}

// Debounce gathers a burst of file changes into one refresh.
const Debounce = 75 * time.Millisecond

// Open starts opening the repository at path and returns at once; State
// says Opening until it is open. update runs a function on the main
// thread (Window.Update).
func Open(runner *run.Runner, ts *trust.Store, path string, update func(func())) *Session {
	s := &Session{Path: path, runner: runner, trust: ts, update: update, State: State{Opening: true},
		wake: make(chan struct{}, 1), timeNow: time.Now, done: make(chan struct{})}
	s.start()
	return s
}

// start opens the repository in the background.
func (s *Session) start() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	s.mu.Lock()
	s.stop, s.opened = cancel, nil
	s.mu.Unlock()
	s.running.Add(1)
	go s.open(ctx, cancel)
}

// Done is closed once the session is closed and everything it started
// has stopped.
func (s *Session) Done() <-chan struct{} { return s.done }

func (s *Session) open(ctx context.Context, cancel context.CancelFunc) {
	trusted := func(loc repo.Location) bool { return s.trust != nil && s.trust.Trusted(loc.CommonDir) }
	r, err := repo.Open(ctx, s.runner, s.Path, trusted)
	cancel()
	s.mu.Lock()
	closed := s.closed
	if err == nil && !closed {
		s.opened = r
	}
	s.mu.Unlock()
	if err != nil || closed {
		if r != nil {
			r.Close()
			<-r.Life().Done()
		}
		s.running.Done()
		if err != nil && !closed {
			s.update(func() { s.State = State{Err: err} })
		}
		return
	}
	go func() { <-r.Life().Done(); s.running.Done() }()
	var q *write.Queue
	if !r.Restricted {
		if q, err = write.NewQueue(r.Life(), r.Runner, r.GitDir); err == nil {
			q.Refs = r.Refs()
		}
	}
	notices, werr := r.Watch(nil)
	s.update(func() {
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if closed {
			r.Close()
			return
		}
		s.Repo, s.Queue = r, q
		s.State = State{Restricted: r.Restricted, Fallbacks: r.Caps.Fallbacks(), Operation: repo.InProgress(r.GitDir)}
		if werr != nil {
			s.State.RefreshErr = werr
		}
	})
	r.Life().Go(func(ctx context.Context) { s.refresher(ctx, r) })
	if notices != nil {
		r.Life().Go(func(ctx context.Context) {
			for {
				select {
				case n, ok := <-notices:
					if !ok {
						return
					}
					what := n.What
					if slices.Contains(n.Paths, ".gitmodules") {
						what |= modulesChanged
					}
					s.Refresh(what)
				case <-ctx.Done():
					return
				}
			}
		})
	}
	s.Refresh(repo.Rescan)
}

// Refresh asks for what changed to be read again; bursts are merged.
func (s *Session) Refresh(what repo.Change) {
	s.mu.Lock()
	s.dirty |= what
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Session) refresher(ctx context.Context, r *repo.Repository) {
	var seen refreshSeen
	for {
		select {
		case <-s.wake:
		case <-ctx.Done():
			return
		}
		select {
		case <-time.After(Debounce):
		case <-ctx.Done():
			return
		}
		s.mu.Lock()
		what := s.dirty
		s.dirty = 0
		s.mu.Unlock()
		if what != 0 {
			s.read(ctx, r, what, &seen)
		}
	}
}

// modulesChanged is .gitmodules changing, beside the watcher's own bits.
const modulesChanged repo.Change = 1 << 7

const all = modulesChanged | repo.WorkTreeChanged | repo.IndexChanged | repo.RefsChanged | repo.OperationChanged | repo.ConfigChanged

func (s *Session) read(ctx context.Context, r *repo.Repository, what repo.Change, seen *refreshSeen) {
	if what&repo.Rescan != 0 {
		what |= all
	}
	type result struct {
		head    *refs.Head
		list    *refs.List
		st      *status.Status
		op      *repo.Operation
		wts     []repo.Worktree
		subs    []repo.Submodule
		readWts bool
		readSub bool
		err     error
		readAt  time.Time
		changed bool
	}
	var res result
	fail := func(err error) {
		if err != nil && res.err == nil && !errors.Is(err, context.Canceled) {
			res.err = err
		}
	}
	var key repo.IndexKey
	keyErr := errors.New("not read")
	if what&(repo.WorkTreeChanged|repo.IndexChanged|repo.RefsChanged) != 0 {
		// Taken before status runs, so a write during it shows next time.
		key, keyErr = repo.ReadIndexKey(r.GitDir)
		if keyErr == nil && seen.indexRead && key == seen.index && what&(repo.WorkTreeChanged|repo.RefsChanged) == 0 {
			what &^= repo.IndexChanged
		}
	}
	if what&(repo.RefsChanged|repo.OperationChanged) != 0 {
		if rd := r.Refs(); rd != nil {
			h, err := rd.Head()
			fail(err)
			if err == nil {
				res.head = &h
			}
			l, err := rd.List()
			fail(err)
			if err == nil && l.Equal(seen.refs) {
				l = nil
			} else if err == nil {
				seen.refs = l
			}
			res.list = l
		}
		op := repo.InProgress(r.GitDir)
		res.op = &op
	}
	if what&(repo.RefsChanged|repo.ConfigChanged) != 0 {
		wts, err := r.Worktrees(ctx)
		fail(err)
		res.wts, res.readWts = wts, err == nil
	}
	if what&(modulesChanged|repo.RefsChanged) != 0 {
		subs, err := r.Submodules(ctx)
		fail(err)
		res.subs, res.readSub = subs, err == nil
	}
	if what&(repo.WorkTreeChanged|repo.IndexChanged|repo.RefsChanged) != 0 {
		st, err := status.Read(ctx, r.Runner, r.GitDir, r.WorkTree, r.Caps.HashSize, status.Options{})
		fail(err)
		res.st = st
		res.readAt = s.timeNow()
		if err == nil {
			seen.index, seen.indexRead = key, keyErr == nil
		}
	}
	if ctx.Err() != nil {
		return
	}
	s.update(func() {
		if s.Repo != r {
			return // closed or reopened since
		}
		st := s.State
		if res.head != nil {
			st.Head = *res.head
		}
		if res.list != nil {
			st.Refs = res.list
		}
		if res.st != nil {
			st.Status, st.Refreshed = res.st, res.readAt
		}
		if res.op != nil {
			st.Operation = *res.op
		}
		if res.readWts {
			st.Worktrees = res.wts
		}
		if res.readSub {
			st.Submodules = res.subs
		}
		st.RefreshErr = res.err
		s.State = st
	})
}

// Submit queues a write and refreshes everything once it ends.
func (s *Session) Submit(op write.Op) (*write.Job, error) {
	if s.Queue == nil {
		return nil, errReadOnly
	}
	j := s.Queue.Submit(op)
	s.follow(s.addLog(op.Name, op.Cmd.Line()), j)
	return j, nil
}

// Trust records that the user trusts the repository and opens it again
// with everything allowed.
func (s *Session) Trust() error {
	if s.Repo == nil || s.trust == nil {
		return errors.New("session: nothing to trust")
	}
	if err := s.trust.Set(s.Repo.CommonDir, true); err != nil {
		return err
	}
	old := s.Repo
	s.Repo, s.Queue, s.State = nil, nil, State{Opening: true}
	old.Close()
	s.start()
	return nil
}

// Close ends the session; the repository's goroutines and processes stop.
func (s *Session) Close() {
	s.mu.Lock()
	was := s.closed
	s.closed = true
	r, stop := s.opened, s.stop
	s.mu.Unlock()
	if was {
		return
	}
	stop()
	if r != nil {
		r.Close()
	}
	s.Repo = nil
	go func() { s.running.Wait(); close(s.done) }()
}
