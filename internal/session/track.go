package session

import (
	"context"
	"strings"
	"time"

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
}

// trackTimeout bounds counting: branches far apart in a large history
// take git a while, and the count is only a help.
const trackTimeout = 10 * time.Second

// LoadTrack reads how the branch checked out stands with its upstream,
// off the main thread, and calls done with it on the main thread. A
// load not yet done when another starts is cancelled; a failed one
// reports none.
func (s *Session) LoadTrack(done func(Track)) {
	r := s.Repo
	head := s.State.Head
	if r == nil || head.Detached || head.Unborn || !strings.HasPrefix(head.Target, "refs/heads/") {
		done(Track{})
		return
	}
	s.track.mu.Lock()
	if s.track.cancel != nil {
		s.track.cancel()
	}
	ctx, cancel := context.WithTimeout(r.Life().Context(), trackTimeout)
	s.track.gen++
	gen := s.track.gen
	s.track.cancel = cancel
	s.track.mu.Unlock()
	update := s.update
	r.Life().Go(func(context.Context) {
		defer cancel()
		res, err := r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, Read: true,
			Args: []string{"for-each-ref", "--format=%(upstream)%00%(upstream:track,nobracket)", head.Target}})
		var t Track
		if err == nil {
			up, track, _ := strings.Cut(strings.TrimSpace(string(res.Stdout)), "\x00")
			t.Upstream = up
			t.Ahead, t.Behind, t.Gone = parseTrack(track)
		}
		// A load cancelled for a newer one is dropped by gen below; one
		// timed out reports none rather than leaving its caller waiting.
		update(func() {
			s.track.mu.Lock()
			ok := s.track.gen == gen
			s.track.mu.Unlock()
			if ok {
				done(t)
			}
		})
	})
}
