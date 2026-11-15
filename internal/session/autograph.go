package session

import (
	"context"
	"errors"
	"time"

	"ramus/internal/git/history"
	"ramus/internal/git/repo"
	"ramus/internal/git/write"
	"ramus/locales"
)

// autoGraph is the commit-graph the session writes on its own, in the
// background, when history would be faster with one: git writes it only
// in gc and maintenance, which many repositories rarely run, and history
// ordered without it is read whole.
type autoGraph struct {
	// repo is the repository the state is for: one opened since starts
	// it afresh.
	repo *repo.Repository
	// job is the write queued or running, nil when none is.
	job *write.Job
	// last is when the last write started.
	last time.Time
	// replaced is set once a write rewrote the whole chain, which on a
	// large repository takes a while: once a session.
	replaced bool
	// retry is set while a timer to try again runs.
	retry bool
	// failures counts the writes failed in a row.
	failures int
}

const (
	// graphEvery is the least time between two writes the session starts
	// on its own: as commits land, a layer of a few at a time is not
	// worth writing.
	graphEvery = 3 * time.Minute
	// graphRetry is how long a write waits for the user's writes, or the
	// lock another git holds, to be done.
	graphRetry = 30 * time.Second
)

// setGraphEvery replaces graphEvery and graphRetry, for tests; every
// below 0 writes none.
func (s *Session) setGraphEvery(every, retry time.Duration) {
	s.graphEvery, s.graphRetry = every, retry
}

// writeGraphQuietly writes the commit-graph a load of history advised,
// a: unless the user's writes run or wait (it never holds them up), one
// started too lately, or the chain was rewritten once already. It is
// tried again later, as it is after failing, as when another git holds
// its lock. What it does shows in the command log, never as an error.
// Main thread only.
func (s *Session) writeGraphQuietly(a *history.Advice) {
	g, r := &s.graph, s.Repo
	if r == nil || s.Queue == nil || r.Restricted {
		return
	}
	if g.repo != r {
		*g = autoGraph{repo: r}
	}
	if a == nil || g.job != nil || a.Replace && g.replaced || s.graphEvery < 0 {
		return
	}
	every, retry := graphEvery, graphRetry
	if s.graphEvery > 0 {
		every, retry = s.graphEvery, s.graphRetry
	}
	if wait := every - time.Since(g.last); !g.last.IsZero() && wait > 0 {
		s.retryGraph(r, max(wait, retry))
		return
	}
	if s.Queue.Pending() > 0 {
		s.retryGraph(r, retry)
		return
	}
	op := write.Op{Name: locales.SessionWriteCommitGraph(), Cmd: a.Cmd}
	op.Cmd.Background = true
	j, err := s.submit(op, repo.GraphChanged, true)
	if err != nil {
		return
	}
	g.job, g.last = j, time.Now()
	if a.Replace {
		g.replaced = true
	}
	update := s.update
	go func() {
		<-j.Done()
		_, err := j.Result()
		update(func() {
			if g.job != j {
				return // the repository opened again
			}
			g.job = nil
			switch {
			case err == nil:
				g.failures = 0
			case r.Life().Context().Err() != nil:
			default:
				if a.Replace {
					g.replaced = false
				}
				g.last = time.Time{}
				wait := retry
				if !errors.Is(err, context.Canceled) {
					// Failed, as when another git held the lock: the wait
					// doubles each time, up to every. One that gave its
					// turn up to a write of the user's goes again soon.
					g.failures++
					wait = min(retry<<min(g.failures, 16), every)
				}
				s.retryGraph(r, wait)
			}
		})
	}()
}

// retryGraph tries the write history advises again after wait, unless a
// timer to do so runs already.
func (s *Session) retryGraph(r *repo.Repository, wait time.Duration) {
	g := &s.graph
	if g.retry {
		return
	}
	g.retry = true
	update := s.update
	r.Life().Go(func(ctx context.Context) {
		t := time.NewTimer(wait)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		update(func() {
			if g.repo != r {
				return
			}
			g.retry = false
			if s.Repo == r && s.History.Done {
				s.writeGraphQuietly(s.History.Advice)
			}
		})
	})
}

// WriteCommitGraph writes the commit-graph as history would have it: a
// layer on the chain for the commits it lacks, or, when it lacks what a
// layer cannot add, the chain anew. History loads again from it once it
// is written.
func (s *Session) WriteCommitGraph() error {
	r := s.Repo
	if r == nil || s.Queue == nil {
		return errReadOnly
	}
	cmd := history.GraphWrite(r.GitDir, false)
	if a := s.History.Advice; a != nil {
		cmd = a.Cmd
	}
	_, err := s.Submit(write.Op{Name: locales.SessionWriteCommitGraph(), Cmd: cmd})
	return err
}
