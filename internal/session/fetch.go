package session

import (
	"context"
	"errors"
	"time"

	"ramus/internal/git/repo"
	"ramus/internal/git/write"
	"ramus/locales"
)

// fetchTimeout bounds a background fetch, which no one waits on.
const fetchTimeout = 10 * time.Minute

// backgroundFetch is the fetch a session runs on its own, on the main
// thread but for its timer.
type backgroundFetch struct {
	every time.Duration
	// repo is the repository the timer was set for: one opened since,
	// or opened again once trusted, sets it anew.
	repo *repo.Repository
	stop chan struct{}
	// job is the quiet fetch queued or running, nil when none is.
	job      *write.Job
	stopped  error
	onFailed func(error)
	// started is when the last fetch of either kind started.
	started time.Time
}

// SetBackgroundFetch fetches every remote every so often, 0 for never;
// failed is told, once, when a fetch fails, which ends fetching in the
// background until the repository opens again: a fetch that needs a
// password cannot ask for it. An untrusted repository, or one with no
// remote, never fetches.
func (s *Session) SetBackgroundFetch(every time.Duration, failed func(error)) {
	bf, r := &s.fetch, s.Repo
	bf.onFailed = failed
	if every == bf.every && r == bf.repo {
		return
	}
	bf.halt()
	bf.every, bf.repo = every, r
	if every == 0 || r == nil || r.Restricted || s.Queue == nil || bf.stopped != nil {
		return
	}
	stop := make(chan struct{})
	bf.stop = stop
	update := s.update
	r.Life().Go(func(ctx context.Context) {
		t := time.NewTimer(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case <-t.C:
			}
			update(func() {
				if bf.stop == stop {
					s.fetchNow()
				}
			})
			t.Reset(every)
		}
	})
}

// halt stops the timer, if one runs.
func (bf *backgroundFetch) halt() {
	if bf.stop != nil {
		close(bf.stop)
		bf.stop = nil
	}
}

// FetchEvery is how often the session fetches in the background, 0 for
// never.
func (s *Session) FetchEvery() time.Duration { return s.fetch.every }

// FetchStopped is why the background fetch stopped, nil while it has not.
func (s *Session) FetchStopped() error { return s.fetch.stopped }

// fetchNow starts a background fetch, unless one is running.
func (s *Session) fetchNow() {
	if s.fetch.stopped != nil {
		return
	}
	s.quietFetch(func(err error) {
		if err == nil || errors.Is(err, context.Canceled) {
			return
		}
		bf := &s.fetch
		bf.stopped = err
		bf.halt()
		if bf.onFailed != nil {
			bf.onFailed(err)
		}
	})
}

// FetchFresh fetches every remote now, as the background fetch does,
// unless a fetch started within fresh or one is running: for the branch
// picker, which shows the remote branches as the user opens it. done is
// told how it ended, on the main thread; it is not called when there is
// no fetch to run, nor once the repository was opened again since.
func (s *Session) FetchFresh(fresh time.Duration, done func(error)) bool {
	if !s.fetch.started.IsZero() && time.Since(s.fetch.started) < fresh {
		return false
	}
	return s.quietFetch(done)
}

// Fetching reports whether a quiet fetch is running.
func (s *Session) Fetching() bool { return s.fetch.job != nil }

// quietFetch fetches every remote with no one to ask for a password,
// unless a fetch is running or there is no remote; it reports whether
// it started one.
func (s *Session) quietFetch(done func(error)) bool {
	bf, r := &s.fetch, s.Repo
	if r == nil || r.Restricted || bf.job != nil || len(r.Config().Subsections("remote", "url")) == 0 {
		return false
	}
	op, err := write.Fetch(s.Where(), "", false)
	if err != nil {
		return false
	}
	op.Name = locales.SessionFetchInBackground()
	op.Cmd.Timeout = fetchTimeout
	op.Cmd.NoPrompt = true // no one is there to answer
	j, err := s.Submit(op)
	if err != nil {
		return false
	}
	bf.job, bf.started = j, time.Now()
	update := s.update
	// Not Then, which gives up once the lifetime ends: the queue ends
	// every job it was given, closing too, so this always hears and the
	// fetch never stays running.
	go func() {
		<-j.Done()
		_, err := j.Result()
		update(func() {
			if bf.job != j {
				return // the state was reset since: the repository opened again
			}
			bf.job = nil
			done(err)
		})
	}()
	return true
}
