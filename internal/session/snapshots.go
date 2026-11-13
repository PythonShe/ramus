package session

import (
	"context"
	"errors"
	"time"

	"ramus/internal/git/snapshot"
	"ramus/internal/git/write"
)

var errReadOnly = errors.New("session: the repository is open read-only until you trust it")

// Snapshots reads the work tree's snapshots off the main thread, newest
// first, and hands them to done on it.
func (s *Session) Snapshots(done func([]snapshot.Snapshot, error)) {
	r := s.Repo
	if r == nil {
		done(nil, errors.New("session: the repository is not open"))
		return
	}
	update := s.update
	r.Life().Go(func(context.Context) {
		cat, err := r.CatFile()
		var list []snapshot.Snapshot
		if err == nil {
			list, err = snapshot.List(r.Refs(), cat)
		}
		update(func() { done(list, err) })
	})
}

// SnapshotFiles reads the files snap holds another state of off the main
// thread and hands them to done on it.
func (s *Session) SnapshotFiles(snap snapshot.Snapshot, done func([]snapshot.File, error)) {
	r := s.Repo
	if r == nil {
		done(nil, errors.New("session: the repository is not open"))
		return
	}
	update := s.update
	r.Life().Go(func(context.Context) {
		cat, err := r.CatFile()
		var files []snapshot.File
		if err == nil {
			files, err = snapshot.Files(cat, r.Caps.HashSize, snap)
		}
		update(func() { done(files, err) })
	})
}

// SnapshotNow keeps the whole work tree in a snapshot, ignored files too
// when asked, logged.
func (s *Session) SnapshotNow(ignored bool) (*write.Job, error) {
	if s.Queue == nil {
		return nil, errReadOnly
	}
	reason := "Snapshot now"
	if ignored {
		reason = "Snapshot now, with ignored files"
	}
	j := s.Queue.SnapshotNow(s.Repo.WorkTree, snapshot.Options{Reason: reason, Ignored: ignored})
	s.follow(s.addLog(reason, ""), j)
	return j, nil
}

// Restore writes paths of snap back over the work tree, logged.
func (s *Session) Restore(snap snapshot.Snapshot, paths []string) (*write.Job, error) {
	if s.Queue == nil {
		return nil, errReadOnly
	}
	j := s.Queue.Restore(s.Repo.WorkTree, snap, paths)
	s.follow(s.addLog("Restore snapshot of "+snap.Time.Format(time.DateTime), ""), j)
	return j, nil
}

// Undo undoes the last write the app ran, logged.
func (s *Session) Undo() (*write.Job, error) {
	if s.Queue == nil {
		return nil, errReadOnly
	}
	name, _ := s.Queue.History.Next()
	j := s.Queue.Undo()
	s.follow(s.addLog("Undo "+name, ""), j)
	return j, nil
}

// Redo redoes the last write undone, logged.
func (s *Session) Redo() (*write.Job, error) {
	if s.Queue == nil {
		return nil, errReadOnly
	}
	_, name := s.Queue.History.Next()
	j := s.Queue.Redo()
	s.follow(s.addLog("Redo "+name, ""), j)
	return j, nil
}

// ExpireSnapshots removes, in the background, snapshots older than keep.
func (s *Session) ExpireSnapshots(keep time.Duration) {
	q, r := s.Queue, s.Repo
	if q == nil || r == nil || keep <= 0 {
		return
	}
	cutoff := s.timeNow().Add(-keep)
	r.Life().Go(func(context.Context) {
		cat, err := r.CatFile()
		if err != nil {
			return
		}
		list, err := snapshot.List(r.Refs(), cat)
		if err != nil || len(list) == 0 {
			return
		}
		<-q.Expire(r.WorkTree, list, cutoff).Done()
	})
}
