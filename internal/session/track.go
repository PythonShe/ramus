package session

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"ramus/internal/git/refs"
	"ramus/internal/git/run"
)

// Track is how the branch checked out stands with the branch it tracks,
// as of the last fetch.
type Track struct {
	// Upstream is the branch tracked, as refs/remotes/origin/main; ""
	// for none, or a detached HEAD.
	Upstream      string
	Ahead, Behind int
	// Gone is an upstream the remote deleted.
	Gone bool
	// Branch is the branch this was read for, as refs/heads/main.
	Branch string
}

// trackTimeout bounds counting: branches far apart in a large history
// take git a while, and the count is only a help.
const trackTimeout = 10 * time.Second

// trackLoad is the read of Track running, if any, and the callers
// waiting on it: several views follow the track as the refs change, and
// each must hear back, or it would wait for good.
type trackLoad struct {
	mu      sync.Mutex
	cancel  context.CancelFunc
	gen     int
	refs    *refs.List
	branch  string
	waiters []func(Track)
}

// LoadTrack reads how the branch checked out stands with its upstream,
// off the main thread, and calls done with it on the main thread. A call
// while a read of the same refs and branch runs waits on that read; one
// for newer refs or another branch cancels it, and its callers hear the
// newer read, which names the branch it was read for. A failed read
// reports none.
func (s *Session) LoadTrack(done func(Track)) {
	r := s.Repo
	head := s.State.Head
	if r == nil || head.Detached || head.Unborn || !strings.HasPrefix(head.Target, "refs/heads/") {
		done(Track{Branch: head.Target})
		return
	}
	l := &s.track
	l.mu.Lock()
	if l.cancel != nil && l.refs == s.State.Refs && l.branch == head.Target {
		l.waiters = append(l.waiters, done)
		l.mu.Unlock()
		return
	}
	if l.cancel != nil {
		l.cancel()
	}
	ctx, cancel := context.WithTimeout(r.Life().Context(), trackTimeout)
	l.gen++
	gen := l.gen
	l.cancel, l.refs, l.branch = cancel, s.State.Refs, head.Target
	l.waiters = append(l.waiters, done)
	l.mu.Unlock()
	// finish hands t to every caller waiting, unless a newer read took
	// them over.
	finish := func(t Track) {
		l.mu.Lock()
		if l.gen != gen {
			l.mu.Unlock()
			return
		}
		waiters := l.waiters
		l.cancel, l.refs, l.waiters = nil, nil, nil
		l.mu.Unlock()
		for _, w := range waiters {
			w(t)
		}
	}
	update := s.update
	err := r.Life().Go(func(context.Context) {
		defer cancel()
		res, err := r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, Read: true,
			Args: []string{"for-each-ref", "--format=%(upstream)%00%(upstream:track,nobracket)", head.Target}})
		t := Track{Branch: head.Target}
		if err == nil {
			up, track, _ := strings.Cut(strings.TrimSpace(string(res.Stdout)), "\x00")
			t.Upstream = up
			t.Ahead, t.Behind, t.Gone = parseTrack(track)
		}
		// One timed out reports none rather than leaving its callers
		// waiting.
		update(func() { finish(t) })
	})
	if err != nil {
		// The repository closed: none, rather than callers left waiting.
		cancel()
		finish(Track{Branch: head.Target})
	}
}

// CountPulled counts the commits of the upstream of branch (a full ref
// name) that commit old (hex) did not hold, off the main thread: after a
// pull, what it took in. done hears on the main thread, and not at all
// when git could not count.
func (s *Session) CountPulled(branch, old string, done func(int)) {
	r := s.Repo
	if r == nil || !strings.HasPrefix(branch, "refs/heads/") || old == "" {
		return
	}
	update := s.update
	// git takes @{upstream} after a branch's short name only.
	r.Life().Go(func(ctx context.Context) {
		ctx, cancel := context.WithTimeout(ctx, trackTimeout)
		defer cancel()
		res, err := r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, Read: true,
			Args: []string{"rev-list", "--count", strings.TrimPrefix(branch, "refs/heads/") + "@{upstream}", "^" + old, "--"}})
		if err != nil {
			return
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(res.Stdout)))
		if err != nil {
			return
		}
		update(func() { done(n) })
	})
}
