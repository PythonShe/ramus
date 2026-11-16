package session

import (
	"context"

	"ramus/internal/git/lfs"
	"ramus/internal/git/write"
)

// LFSLocks reads the repository's Git LFS locks from its server off the
// main thread and hands them to done on it. Leaving the view stops the
// read, and done is not called.
func (s *Session) LFSLocks(done func(lfs.Locks, error)) {
	r := s.Repo
	if r == nil {
		return
	}
	if r.Restricted {
		// git-lfs runs the repository's config: not before it is trusted.
		done(lfs.Locks{}, ErrReadOnly)
		return
	}
	update := s.update
	s.goView(r, func(ctx context.Context) {
		ls, err := lfs.List(ctx, r.Runner, r.GitDir, r.WorkTree)
		if ctx.Err() != nil {
			return
		}
		update(func() { done(ls, err) })
	})
}

// LFSLock locks path, relative to the work tree, on the LFS server.
func (s *Session) LFSLock(path string) (*write.Job, error) {
	op, err := write.LFSLock(s.Where(), path)
	if err != nil {
		return nil, err
	}
	return s.Submit(op)
}

// LFSUnlock releases l; force releases a lock someone else holds.
func (s *Session) LFSUnlock(l lfs.Lock, force bool) (*write.Job, error) {
	op, err := write.LFSUnlock(s.Where(), l.Path, l.ID, force)
	if err != nil {
		return nil, err
	}
	return s.Submit(op)
}
