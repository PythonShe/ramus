package write

import (
	"context"
	"errors"
	"time"

	"ramus/internal/git/run"
	"ramus/internal/git/snapshot"
)

// SnapshotNow queues keeping the work tree at workTree in a snapshot on
// demand ("Snapshot now", before handing the repository to an agent).
// Job.Kept returns it.
func (q *Queue) SnapshotNow(workTree string, opts snapshot.Options) *Job {
	w := snapshot.Where{GitDir: q.gitDir, WorkTree: workTree}
	return q.Submit(Op{Name: "Snapshot", do: func(ctx context.Context, j *Job) (run.Result, error) {
		s, err := snapshot.Take(ctx, q.r, w, opts)
		if err == nil {
			j.keep(s)
		}
		return run.Result{}, err
	}})
}

// Restore queues writing paths of s back over the work tree, leaving every
// other file alone (snapshot.Overlay); with no paths, the paths s was
// taken of (a snapshot of the whole work tree needs its files named).
// The state it replaces is kept first, and Job.Kept returns that.
func (q *Queue) Restore(workTree string, s snapshot.Snapshot, paths []string) *Job {
	w := snapshot.Where{GitDir: q.gitDir, WorkTree: workTree}
	return q.Submit(Op{Name: "Restore snapshot", do: func(ctx context.Context, j *Job) (run.Result, error) {
		if len(paths) == 0 {
			paths = s.Paths
		}
		if len(paths) == 0 {
			return run.Result{}, errors.New("write: choose the files to restore")
		}
		before, err := snapshot.Restore(ctx, q.r, w, s, paths, snapshot.Overlay)
		if err == nil {
			j.keep(before)
		}
		return run.Result{}, err
	}})
}

// Expire queues removing the snapshots in list taken before cutoff.
func (q *Queue) Expire(workTree string, list []snapshot.Snapshot, cutoff time.Time) *Job {
	w := snapshot.Where{GitDir: q.gitDir, WorkTree: workTree}
	return q.Submit(Op{Name: "Expire snapshots", do: func(ctx context.Context, _ *Job) (run.Result, error) {
		_, err := snapshot.Expire(ctx, q.r, w, list, cutoff)
		return run.Result{}, err
	}})
}

func (j *Job) keep(s snapshot.Snapshot) {
	j.mu.Lock()
	j.snap = s
	j.mu.Unlock()
}
