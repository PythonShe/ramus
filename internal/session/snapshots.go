package session

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"ramus/internal/git/snapshot"
	"ramus/internal/git/write"
	"ramus/locales"
)

// ErrReadOnly is a write asked of a repository not trusted.
var ErrReadOnly = errors.New("session: the repository is open read-only until you trust it")

// noWrites is why the session cannot write: the repository is not
// trusted, or its writes are held while another program works there.
func (s *Session) noWrites() error {
	if s.holding {
		return errors.New(locales.RepoHeldBanner())
	}
	return ErrReadOnly
}

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
// when asked, logged, named name, or when name is blank by where it was
// taken: the branch, the commit and how many files were changed, which
// tells apart snapshots taken on the same commit.
func (s *Session) SnapshotNow(name string, ignored bool) (*write.Job, error) {
	if s.Queue == nil {
		return nil, s.noWrites()
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = s.snapshotName()
	}
	j := s.Queue.SnapshotNow(s.Repo.WorkTree, snapshot.Options{Reason: name, Ignored: ignored})
	log := locales.SessionSnapshotNow(name)
	if ignored {
		log = locales.SessionSnapshotNowIgnored(name)
	}
	s.follow(s.addLog(log, ""), j)
	return j, nil
}

// snapshotName names a snapshot of the work tree as it is now.
func (s *Session) snapshotName() string {
	head := s.State.Head
	commit := hex.EncodeToString(head.OID)[:min(8, 2*len(head.OID))]
	branch := ""
	if !head.Detached {
		branch = strings.TrimPrefix(head.Target, "refs/heads/")
	}
	changed := 0
	if st := s.State.Status; st != nil {
		changed = st.Len()
	}
	switch {
	case commit == "":
		return locales.SessionSnapshotNameUnborn(branch, changed)
	case branch == "":
		return locales.SessionSnapshotNameDetached(commit, changed)
	}
	return locales.SessionSnapshotName(branch, commit, changed)
}

// DeleteSnapshot removes snap, logged: its files can no longer be
// restored.
func (s *Session) DeleteSnapshot(snap snapshot.Snapshot) (*write.Job, error) {
	if s.Queue == nil {
		return nil, s.noWrites()
	}
	j := s.Queue.DeleteSnapshots(s.Repo.WorkTree, []snapshot.Snapshot{snap})
	s.follow(s.addLog(locales.SessionDeleteSnapshot(snap.Reason), ""), j)
	return j, nil
}

// Restore writes paths of snap back over the work tree, logged.
func (s *Session) Restore(snap snapshot.Snapshot, paths []string) (*write.Job, error) {
	if s.Queue == nil {
		return nil, s.noWrites()
	}
	j := s.Queue.Restore(s.Repo.WorkTree, snap, paths)
	s.follow(s.addLog(locales.SessionRestoreSnapshot(snap.Time.Format(time.DateTime)), ""), j)
	return j, nil
}

// Undo undoes the last write the app ran, logged.
func (s *Session) Undo() (*write.Job, error) {
	if s.Queue == nil {
		return nil, s.noWrites()
	}
	name, _ := s.Queue.History.Next()
	j := s.Queue.Undo()
	s.follow(s.addLog(locales.SessionUndo(name), ""), j)
	return j, nil
}

// Redo redoes the last write undone, logged.
func (s *Session) Redo() (*write.Job, error) {
	if s.Queue == nil {
		return nil, s.noWrites()
	}
	_, name := s.Queue.History.Next()
	j := s.Queue.Redo()
	s.follow(s.addLog(locales.SessionRedo(name), ""), j)
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
