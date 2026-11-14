// Package session is one open repository as the app sees it: opened off
// the main thread, kept fresh from the watcher, with its write queue. It
// knows nothing of the UI: what it learns it hands to update, which runs
// on the main thread, and the UI reads State only there.
package session

import (
	"bytes"
	"context"
	"errors"
	"ramus/internal/git/cache"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"ramus/internal/git/lifetime"
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
	// WatchErr is why changes are not watched, when they are not: the
	// window gaining focus then reads everything again.
	WatchErr error
	// Refreshed is when status was last read.
	Refreshed time.Time
	// Monitor offers git's file system monitor and the untracked cache
	// when status was slow.
	Monitor *status.MonitorAdvice
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
	// Panic is the first panic recovered in the repository's work: what
	// it was doing may have stopped, so the tab offers to open it again.
	Panic *lifetime.Panic

	mu    sync.Mutex
	dirty repo.Change
	// dirtyPaths are the work-tree paths the merged changes named, unless
	// one named none (dirtyAll).
	dirtyPaths []string
	dirtyAll   bool
	// now asks the next refresh not to wait for changes to go quiet: a
	// write of the app's own has ended, and the user waits on it.
	now     bool
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
	patch   PatchSlot
	hist    hist
	// historyView is what history shows, numbered by historyViewN.
	historyView  HistoryView
	historyViewN int
	// commitPatch cancels a commit patch read that a newer one replaced.
	commitPatch PatchSlot
	// answerCache holds what views read, on the main thread; see answers.
	answerCache *cache.Cache[string, answer]
	stats       statsLoad
	tools       diffTools
	refInfo     refLoad
	blame       blames
	view        viewScope
	fetch       backgroundFetch
	// gitDir is the repository's, once open, for the crash log.
	gitDir atomic.Pointer[string]
	// monitorAnswered is set once the user turned the monitor on or said
	// not now; the refresher reads it.
	monitorAnswered atomic.Bool
}

// refreshSeen is what the last refresh of one repository read, to tell what a change
// notice really changed: an index rewritten with the same content key
// needs no status, and refs read with the same values keep the list
// everything was built from.
type refreshSeen struct {
	index     repo.IndexKey
	indexRead bool
	refs      *refs.List
	// status is the last status published, for a recheck to merge into.
	status *status.Status
	head   *refs.Head
}

// focusChanges is what changed of refs, HEAD and the index since they
// were last read, by reading them, not status.
func focusChanges(r *repo.Repository, seen *refreshSeen) repo.Change {
	var what repo.Change
	if key, err := repo.ReadIndexKey(r.GitDir); err != nil || !seen.indexRead || key != seen.index {
		what |= repo.IndexChanged
	}
	rd := r.Refs()
	if rd == nil {
		return what | repo.RefsChanged
	}
	h, err := rd.Head()
	l, lerr := rd.List()
	if err != nil || lerr != nil || seen.head == nil || !sameHead(h, *seen.head) || !l.Equal(seen.refs) {
		what |= repo.RefsChanged | repo.OperationChanged
	}
	return what
}

func sameHead(a, b refs.Head) bool {
	return a.Target == b.Target && bytes.Equal(a.OID, b.OID) && a.Detached == b.Detached && a.Unborn == b.Unborn
}

// Debounce gathers a burst of file changes into one refresh: a refresh
// waits until no change came for Debounce, or MaxDebounce after the
// first, so that a tool writing thousands of files (a coding agent, a
// formatter) costs one status, and one that never stops still shows.
const (
	Debounce    = 75 * time.Millisecond
	MaxDebounce = time.Second
)

// Open starts opening the repository at path and returns at once; State
// says Opening until it is open. update runs a function on the main
// thread (Window.Update).
func Open(runner *run.Runner, ts *trust.Store, path string, update func(func())) *Session {
	s := &Session{Path: path, runner: runner, trust: ts, State: State{Opening: true},
		wake: make(chan struct{}, 1), timeNow: time.Now, done: make(chan struct{})}
	s.update = func(fn func()) {
		update(func() {
			defer s.recoverMain()
			fn()
		})
	}
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
	defer s.recoverWork()
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
	s.gitDir.Store(&r.GitDir)
	r.Life().OnPanic(s.panicked)
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
		s.State.WatchErr = werr
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
					if err := n.Err; err != nil {
						s.update(func() {
							if s.Repo == r {
								s.State.WatchErr = err
							}
						})
					}
					s.refreshPaths(what, n.Paths)
				case <-ctx.Done():
					return
				}
			}
		})
	}
	s.Refresh(repo.Rescan)
}

// Refresh asks for what changed to be read again; bursts are merged.
func (s *Session) Refresh(what repo.Change) { s.refreshPaths(what, nil) }

// refreshNow is Refresh at once, for the end of a write the user waits
// on: what it changed shows before the user acts again on what was.
func (s *Session) refreshNow(what repo.Change) {
	s.mu.Lock()
	s.now = true
	s.mu.Unlock()
	s.Refresh(what)
}

func (s *Session) urgent() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.now
}

// Reload reads everything again, for a refresh the user asks for: the
// config, refs, a full status and history, with the patches and commits
// kept dropped. Main thread only.
func (s *Session) Reload() {
	s.DropAnswers()
	s.historyViewN++
	s.Refresh(repo.Rescan)
}

// FocusRefresh is the window gaining focus, a fallback for changes the
// watcher missed: while it watches, refs and the index's key are read,
// and status only when they changed; when it does not, everything.
// Main thread only.
func (s *Session) FocusRefresh() {
	if s.State.WatchErr != nil {
		s.Refresh(repo.Rescan)
		return
	}
	s.Refresh(focusCheck)
}

// refreshPaths is Refresh for work-tree changes the watcher named: while
// every change merged into the next refresh names its paths, and they are
// few, status reads only them.
func (s *Session) refreshPaths(what repo.Change, paths []string) {
	s.mu.Lock()
	s.dirty |= what
	switch {
	case what&(repo.WorkTreeChanged|repo.Rescan) == 0:
	case paths == nil || what&repo.Rescan != 0 || s.dirtyAll:
		s.dirtyAll, s.dirtyPaths = true, nil
	case len(s.dirtyPaths)+len(paths) > status.MaxRecheckPaths:
		s.dirtyAll, s.dirtyPaths = true, nil
	default:
		s.dirtyPaths = append(s.dirtyPaths, paths...)
	}
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
		if !quiet(ctx, s.wake, s.urgent) {
			return
		}
		s.mu.Lock()
		what, paths := s.dirty, s.dirtyPaths
		if s.dirtyAll {
			paths = nil
		}
		s.dirty, s.dirtyPaths, s.dirtyAll, s.now = 0, nil, false, false
		s.mu.Unlock()
		if what != 0 {
			s.read(ctx, r, what, paths, &seen)
		}
	}
}

// quiet waits until wake has been still for Debounce, or MaxDebounce
// has passed, or urgent holds; false when ctx ended first.
func quiet(ctx context.Context, wake <-chan struct{}, urgent func() bool) bool {
	if urgent() {
		return true
	}
	limit := time.NewTimer(MaxDebounce)
	defer limit.Stop()
	still := time.NewTimer(Debounce)
	defer still.Stop()
	for {
		select {
		case <-wake:
			if urgent() {
				return true
			}
			still.Reset(Debounce)
		case <-still.C:
			return true
		case <-limit.C:
			return true
		case <-ctx.Done():
			return false
		}
	}
}

// modulesChanged is .gitmodules changing, beside the watcher's own bits.
const modulesChanged repo.Change = 1 << 7

// focusCheck asks whether refs, HEAD or the index changed unseen; what
// did is read.
const focusCheck repo.Change = 1 << 8

const all = modulesChanged | repo.WorkTreeChanged | repo.IndexChanged | repo.RefsChanged | repo.OperationChanged | repo.ConfigChanged

func (s *Session) read(ctx context.Context, r *repo.Repository, what repo.Change, paths []string, seen *refreshSeen) {
	if what&repo.Rescan != 0 {
		what |= all
	}
	if what&focusCheck != 0 {
		what = what&^focusCheck | focusChanges(r, seen)
		if what == 0 {
			return
		}
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
		monitor *status.MonitorAdvice
	}
	var res result
	fail := func(err error) {
		if err != nil && res.err == nil && !errors.Is(err, context.Canceled) {
			res.err = err
		}
	}
	if what&repo.ConfigChanged != 0 {
		// Before status, which may run what the config names.
		fail(r.ReloadConfig(ctx))
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
			prev := seen.head
			h, err := rd.Head()
			fail(err)
			// A detached HEAD that moved, or one that detached or came
			// back to a branch, changes no ref, yet history starts from it:
			// a new list has it read again.
			headMoved := false
			if err == nil {
				headMoved = prev != nil && (prev.Detached != h.Detached || h.Detached && !bytes.Equal(prev.OID, h.OID))
				res.head = &h
				seen.head = &h
			}
			l, err := rd.List()
			fail(err)
			if err == nil && l.Equal(seen.refs) && !headMoved {
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
		start := time.Now()
		var st *status.Status
		var err error
		rechecked := false
		if what == repo.WorkTreeChanged && seen.status != nil && status.CanRecheck(paths) {
			// Edits to files git knows: read those alone and merge.
			var part *status.Status
			if part, err = status.ReadPaths(ctx, r.Runner, r.GitDir, r.WorkTree, r.Caps.HashSize, paths); err == nil {
				st, rechecked = seen.status.Replace(paths, part)
			}
		}
		if !rechecked {
			st, err = status.Read(ctx, r.Runner, r.GitDir, r.WorkTree, r.Caps.HashSize, status.Options{})
		}
		fail(err)
		res.st = st
		res.readAt = s.timeNow()
		if err == nil {
			seen.status = st
		}
		if err == nil && !rechecked && !s.monitorAnswered.Load() {
			if a, ok := status.AdviseMonitor(runtime.GOOS, r.Runner.Git().Version, r.Config(), r.GitDir, r.WorkTree, !r.Restricted, time.Since(start)); ok {
				res.monitor = &a
			}
		}
		if err == nil && !rechecked {
			// A recheck read the work tree only; an index notice still
			// to come must not find its key already seen.
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
			if st.Monitor == nil && !s.monitorAnswered.Load() {
				// Offered once status was slow, kept while status is fast
				// from a warm cache.
				st.Monitor = res.monitor
			}
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
	line := op.Cmd.Line()
	if op.Line != "" {
		line = op.Line
	}
	for _, c := range op.More {
		line += "\n" + c.Line()
	}
	s.follow(s.addLog(op.Name, line), j)
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
