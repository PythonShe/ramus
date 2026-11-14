package session

import (
	"context"
	"errors"
	"strings"

	"ramus/internal/git/run"
	"ramus/internal/git/write"
)

// Then calls fn on the main thread with the result of job j once it ends.
func (s *Session) Then(j *write.Job, fn func(error)) {
	r := s.Repo
	if r == nil || j == nil {
		return
	}
	r.Life().Go(func(ctx context.Context) {
		select {
		case <-j.Done():
		case <-ctx.Done():
			return
		}
		_, err := j.Result()
		s.update(func() { fn(err) })
	})
}

// PushedRewrite checks, after branch (full name) was rewritten from old
// (hex), whether its upstream had been pushed from what the branch was
// and the branch no longer contains it: pushing it now takes a force.
// done gets the upstream, as "origin/main", on the main thread only then.
func (s *Session) PushedRewrite(branch, old string, done func(upstream string)) {
	r := s.Repo
	if r == nil || branch == "" || old == "" {
		return
	}
	update := s.update
	r.Life().Go(func(ctx context.Context) {
		res, err := r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, Read: true, Untranslated: true,
			Args: []string{"rev-parse", "--verify", "--quiet", "--symbolic-full-name", "--end-of-options",
				strings.TrimPrefix(branch, "refs/heads/") + "@{upstream}"}})
		up := strings.TrimSpace(string(res.Stdout))
		if err != nil || !strings.HasPrefix(up, "refs/remotes/") {
			return
		}
		ancestor := func(of string) (bool, error) {
			_, err := r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, Read: true,
				Args: []string{"merge-base", "--is-ancestor", "--end-of-options", up, of}})
			var e *run.Error
			if errors.As(err, &e) && e.ExitCode == 1 {
				return false, nil
			}
			return err == nil, err
		}
		was, err := ancestor(old)
		if err != nil || !was {
			return // not pushed, or someone else's push not taken in
		}
		if still, err := ancestor(branch); err != nil || still {
			return
		}
		update(func() { done(strings.TrimPrefix(up, "refs/remotes/")) })
	})
}
