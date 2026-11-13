package session

import (
	"context"
	"errors"
	"sync"

	"ramus/internal/git/diff"
	"ramus/internal/git/write"
)

// Where names the open repository for a write.
func (s *Session) Where() write.Where {
	if s.Repo == nil {
		return write.Where{}
	}
	return write.Where{GitDir: s.Repo.GitDir, WorkTree: s.Repo.WorkTree}
}

// PatchResult is a patch read for the changes view.
type PatchResult struct {
	Parsed    *diff.Parsed
	Truncated bool
	Err       error
}

// patches cancels a patch read that a newer one replaced.
type patches struct {
	mu     sync.Mutex
	cancel context.CancelFunc
}

// Patch reads a work-tree patch off the main thread, in the form git
// apply takes back, and hands it to done on it. A newer call cancels a
// read still running, whose done is not called.
func (s *Session) Patch(req diff.PatchRequest, done func(PatchResult)) {
	r := s.Repo
	if r == nil {
		done(PatchResult{Err: errors.New("session: the repository is not open")})
		return
	}
	req.ForApply = true
	update := s.update
	s.patch.mu.Lock()
	if s.patch.cancel != nil {
		s.patch.cancel()
	}
	ctx, cancel := context.WithCancel(r.Life().Context())
	s.patch.cancel = cancel
	s.patch.mu.Unlock()
	r.Life().Go(func(context.Context) {
		defer cancel()
		pt, err := diff.WorkTreePatch(ctx, r.Runner, r.GitDir, r.WorkTree, req)
		var res PatchResult
		if err == nil {
			res.Truncated = pt.Truncated
			res.Parsed, err = diff.Parse(pt.Text)
		}
		res.Err = err
		if ctx.Err() != nil {
			return
		}
		update(func() { done(res) })
	})
}
