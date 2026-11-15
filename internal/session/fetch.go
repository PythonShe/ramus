package session

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"time"

	"ramus/internal/git/lifetime"
	"ramus/internal/git/repo"
	"ramus/internal/git/run"
	"ramus/internal/git/write"
	"ramus/locales"
)

// fetchTimeout bounds a background fetch, which no one waits on.
const fetchTimeout = 10 * time.Minute

// fetchBusyWait is how soon a background fetch put off while another
// worktree was busy is tried again.
const fetchBusyWait = time.Minute

// backgroundFetch is the fetch a session runs on its own, on the main
// thread but for its timer.
type backgroundFetch struct {
	every time.Duration
	// repo is the repository the timer was set for: one opened since,
	// or opened again once trusted, sets it anew.
	repo *repo.Repository
	stop chan struct{}
	// job is the quiet fetch queued or running, nil when none is.
	job     *write.Job
	stopped error
	// failures counts the background fetches failed in a row.
	failures int
	onFailed func(error)
	// started is when the last fetch of either kind started.
	started time.Time
	// waited counts the fetches put off for another worktree's git, for
	// a test to tell one put off from one not yet due.
	waited atomic.Int32
}

// SetBackgroundFetch fetches every remote every so often, 0 for never.
// A fetch that fails for want of the user (a login, a password, a host
// key: it cannot ask, see needsUser) ends fetching in the background
// until the repository opens again, and failed is told, once. Any other
// failure, as the network being down, is tried again, the wait doubling
// each time up to maxFetchWait. While git works in another worktree of
// the repository (othersBusy), the fetch waits for it. An untrusted
// repository, one held (HoldWrites) or one with no remote never
// fetches.
func (s *Session) SetBackgroundFetch(every time.Duration, failed func(error)) {
	bf, r := &s.fetch, s.Repo
	bf.onFailed = failed
	if every == bf.every && r == bf.repo {
		return
	}
	bf.halt()
	bf.every, bf.repo, bf.failures = every, r, 0
	if every == 0 || r == nil || r.Restricted || s.Queue == nil || bf.stopped != nil {
		return
	}
	stop := make(chan struct{})
	bf.stop = stop
	update := s.update
	busyWait := min(every, fetchBusyWait)
	if s.fetchBusyWait > 0 {
		busyWait = s.fetchBusyWait
	}
	r.Life().Go(func(ctx context.Context) {
		wait := every
		for {
			t := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-stop:
				t.Stop()
				return
			case <-t.C:
			}
			// Not while git works in another worktree, as an agent's
			// commit or fetch: both would lock the same refs, and the
			// agent's would fail.
			if othersBusy(ctx, r) {
				bf.waited.Add(1)
				wait = busyWait
				continue
			}
			// The next wait is known once the fetch has ended.
			next := make(chan time.Duration, 1)
			update(func() {
				if bf.stop == stop {
					s.fetchNow(func(d time.Duration) { next <- d })
				}
			})
			select {
			case wait = <-next:
			case <-ctx.Done():
				return
			case <-stop:
				return
			}
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

// maxFetchWait bounds the wait between background fetches that failed,
// unless fetching is set to be rarer still.
const maxFetchWait = time.Hour

// wait is how long to wait before the next background fetch: the
// interval set, doubled for each fetch failed in a row.
func (bf *backgroundFetch) wait() time.Duration {
	limit := max(bf.every, maxFetchWait)
	d := bf.every
	for i := 0; i < bf.failures && d < limit; i++ {
		d *= 2
	}
	return min(d, limit)
}

// FetchEvery is how often the session fetches in the background, 0 for
// never.
func (s *Session) FetchEvery() time.Duration { return s.fetch.every }

// FetchStopped is why the background fetch stopped, nil while it has not.
func (s *Session) FetchStopped() error { return s.fetch.stopped }

// fetchNow starts a background fetch, unless one is running, and hands
// next the wait before the one after, once this one has ended.
func (s *Session) fetchNow(next func(time.Duration)) {
	bf := &s.fetch
	if bf.stopped != nil {
		return
	}
	started := s.quietFetch(func(err error) {
		switch {
		case err == nil:
			bf.failures = 0
		case errors.Is(err, context.Canceled), errors.Is(err, lifetime.ErrClosed):
			// Stopped, or the repository closed: that says nothing of
			// the remote.
		case needsUser(err):
			bf.stopped = err
			bf.halt()
			if bf.onFailed != nil {
				bf.onFailed(err)
			}
			return
		default:
			bf.failures++
		}
		next(bf.wait())
	})
	if !started {
		next(bf.wait())
	}
}

// needsUser reports a fetch that failed for want of something only the
// user can give: credentials, which a fetch with no one to ask cannot
// read, a login the server refused, or a host key ssh does not know or
// that changed. Trying again would fail the same way, and some servers
// count each try towards locking the account. The fetch runs
// Untranslated, so git's own words are English; ssh's always are.
func needsUser(err error) bool {
	var re *run.Error
	if !errors.As(err, &re) {
		return false
	}
	msg := strings.ToLower(re.Stderr)
	for _, s := range []string{
		"terminal prompts disabled", // could not read Username for ...
		"could not read username",
		"could not read password",
		"authentication failed",
		"invalid username or password",
		"permission denied (", // ssh: Permission denied (publickey).
		"host key verification failed",
		"remote host identification has changed",
		"returned error: 401",
		"returned error: 403",
		// GitHub and others answer so for a private repository to one
		// not logged in.
		"repository not found",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
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
	// needsUser reads what git said.
	op.Cmd.Untranslated = true
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
