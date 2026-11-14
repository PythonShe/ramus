package session

import (
	"context"

	"ramus/internal/git/repo"
)

// viewScope is the context of the reads the shown view started: leaving
// the view cancels them, and the git processes they run end with them
// (GitLens #3307, #4074). It is used on the main thread only.
type viewScope struct {
	name   string
	r      *repo.Repository
	ctx    context.Context
	cancel context.CancelFunc
}

// SetView says which view of the repository is shown, "" for none (the
// tab is in the background). Reads started for another view stop; their
// answers do not come.
func (s *Session) SetView(name string) {
	if name == s.view.name {
		return
	}
	if s.view.cancel != nil {
		s.view.cancel()
	}
	s.view = viewScope{name: name}
}

// viewContext is the shown view's context in repository r.
func (s *Session) viewContext(r *repo.Repository) context.Context {
	if s.view.r != r || s.view.ctx == nil {
		if s.view.cancel != nil {
			s.view.cancel()
		}
		ctx, cancel := context.WithCancel(r.Life().Context())
		s.view = viewScope{name: s.view.name, r: r, ctx: ctx, cancel: cancel}
	}
	return s.view.ctx
}

// goView runs fn in r's lifetime with the shown view's context: it is
// cancelled when the view is left or the repository closes.
func (s *Session) goView(r *repo.Repository, fn func(ctx context.Context)) {
	ctx := s.viewContext(r)
	r.Life().Go(func(context.Context) { fn(ctx) })
}
