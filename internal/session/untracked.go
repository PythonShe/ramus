package session

import (
	"context"
	"errors"

	"ramus/internal/git/repo"
)

// UntrackedIn lists the files git does not track under dir, the first
// max of them, off the main thread and hands them to done on it. cancel
// stops the read, as leaving the view does; done is not called then.
func (s *Session) UntrackedIn(dir string, max int, done func(repo.UntrackedFiles, error)) (cancel func()) {
	r := s.Repo
	if r == nil {
		done(repo.UntrackedFiles{}, errors.New("session: the repository is not open"))
		return func() {}
	}
	ctx, stop := context.WithCancel(s.viewContext(r))
	update := s.update
	err := r.Life().Go(func(context.Context) {
		files, err := r.UntrackedUnder(ctx, dir, max)
		if ctx.Err() != nil {
			stop()
			return
		}
		update(func() {
			live := ctx.Err() == nil
			stop()
			if live {
				done(files, err)
			}
		})
	})
	if err != nil {
		stop()
	}
	return stop
}
