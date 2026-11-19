package write

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
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

// DeleteSnapshots queues removing the snapshots in list.
func (q *Queue) DeleteSnapshots(workTree string, list []snapshot.Snapshot) *Job {
	w := snapshot.Where{GitDir: q.gitDir, WorkTree: workTree}
	return q.Submit(Op{Name: "Delete snapshots", do: func(ctx context.Context, _ *Job) (run.Result, error) {
		return run.Result{}, snapshot.Delete(ctx, q.r, w, list)
	}})
}

// Expire queues removing the snapshots in list taken before cutoff, and
// the backups made before it but those a write Undo or Redo can still
// reach needs.
func (q *Queue) Expire(workTree string, list []snapshot.Snapshot, cutoff time.Time) *Job {
	w := snapshot.Where{GitDir: q.gitDir, WorkTree: workTree}
	return q.Submit(Op{Name: "Expire snapshots", do: func(ctx context.Context, _ *Job) (run.Result, error) {
		_, err := snapshot.Expire(ctx, q.r, w, list, cutoff)
		if berr := q.expireBackups(ctx, cutoff); err == nil {
			err = berr
		}
		return run.Result{}, err
	}})
}

// expireBackups removes the backups made before cutoff, by the time in
// their directory's name, in one transaction, each at the value listed,
// leaving those the History holds.
func (q *Queue) expireBackups(ctx context.Context, cutoff time.Time) error {
	res, err := q.r.Run(ctx, &run.Cmd{GitDir: q.gitDir, Read: true,
		Args: []string{"for-each-ref", "--format=%(refname) %(objectname)", BackupPrefix}})
	if err != nil {
		return err
	}
	keep := q.History.backups()
	var in strings.Builder
	for line := range strings.SplitSeq(string(res.Stdout), "\n") {
		name, oid, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		stamp, _, _ := strings.Cut(strings.TrimPrefix(name, BackupPrefix), "/")
		ns, err := strconv.ParseInt(stamp, 10, 64)
		if err != nil || keep[BackupPrefix+stamp+"/"] || !time.Unix(0, ns).Before(cutoff) {
			continue
		}
		fmt.Fprintf(&in, "delete %s %s\n", name, oid)
	}
	if in.Len() == 0 {
		return nil
	}
	_, err = q.r.Run(ctx, &run.Cmd{GitDir: q.gitDir, Stdin: strings.NewReader(in.String()),
		Args: []string{"update-ref", "--stdin"}})
	return err
}

func (j *Job) keep(s snapshot.Snapshot) {
	j.mu.Lock()
	j.snap = s
	j.mu.Unlock()
}
