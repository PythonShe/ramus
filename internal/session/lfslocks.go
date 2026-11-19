package session

import (
	"context"

	"ramus/internal/git/lfs"
	"ramus/internal/git/write"
)

// LFSLocks reads the repository's Git LFS locks from its server off the
// main thread and hands them to done on it. Leaving the view stops the
// read, and done is not called; a read whose repository closed and opened
// again (Trust) is answered ErrReopened.
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
	shown := s.stillShown()
	s.goView(r, func(ctx context.Context) {
		s.lfsMu.Lock()
		ls, err := lfs.List(ctx, r.Runner, r.GitDir, r.WorkTree)
		s.lfsMu.Unlock()
		if ctx.Err() != nil && !closedUnder(r) {
			return
		}
		s.handOver(r, func() {
			if ctx.Err() == nil {
				done(ls, err)
			}
		}, func() {
			if shown() {
				done(lfs.Locks{}, ErrReopened)
			}
		})
	})
}

// LFSLock locks path, relative to the work tree, on the LFS server.
func (s *Session) LFSLock(path string) (*write.Job, error) {
	op, err := write.LFSLock(s.Where(), path)
	if err != nil {
		return nil, err
	}
	op.Hold = &s.lfsMu
	return s.Submit(op)
}

// LFSUnlock releases l; force releases a lock someone else holds.
func (s *Session) LFSUnlock(l lfs.Lock, force bool) (*write.Job, error) {
	op, err := write.LFSUnlock(s.Where(), l.Path, l.ID, force)
	if err != nil {
		return nil, err
	}
	op.Hold = &s.lfsMu
	return s.Submit(op)
}
