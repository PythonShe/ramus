package session

import (
	"context"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"ramus/internal/forge"
	"ramus/internal/git/run"
	"ramus/internal/git/write"
)

// ReviewRepo is the repository on GitHub or GitLab whose pull requests
// the repository's are, as its remotes name it: the remote named
// upstream, when it is on a forge, else origin (or the only remote). ok
// is false when none is on one. A fork's parent, which no remote need
// name, is found when the pull requests are listed (OpenPulls).
func (s *Session) ReviewRepo() (forge.Repo, bool) {
	r := s.Repo
	if r == nil || r.Restricted {
		return forge.Repo{}, false
	}
	up, origin := RemoteURLs(r)
	if p, ok := forge.Parse(up); ok {
		return p, true
	}
	return forge.Parse(origin)
}

// OpenPullsResult is a repository's open pull requests, and the
// repository they are in.
type OpenPullsResult struct {
	Repo forge.Repo
	List []forge.Listed
	More bool
	Err  error
}

// OpenPulls lists the open pull requests of ReviewRepo, or of the
// repository it was forked from, off the main thread, through the tool
// find gives, and hands them to done on it. Leaving the view drops the
// answer.
func (s *Session) OpenPulls(find func(forge.Kind) (*forge.CLI, error), done func(OpenPullsResult)) {
	r := s.Repo
	repo, ok := s.ReviewRepo()
	if !ok {
		done(OpenPullsResult{Err: forge.ErrNoReview})
		return
	}
	up, _ := RemoteURLs(r)
	_, named := forge.Parse(up)
	update := s.update
	ctx := s.viewContext(r)
	r.Life().Go(func(context.Context) {
		res := OpenPullsResult{Repo: repo}
		c, err := find(repo.Kind)
		if err == nil {
			if !named {
				res.Repo = s.forks.parentOf(ctx, c, repo)
			}
			res.List, res.More, err = c.OpenPulls(ctx, res.Repo)
		}
		res.Err = err
		if ctx.Err() != nil {
			return
		}
		update(func() {
			if ctx.Err() == nil {
				done(res)
			}
		})
	})
}

// PullReview reads pull request n of repo with its conversation and
// review threads off the main thread, and hands it to done on it.
// Leaving the view drops the answer.
func (s *Session) PullReview(find func(forge.Kind) (*forge.CLI, error), repo forge.Repo, n int, done func(forge.Review, error)) {
	r := s.Repo
	if r == nil || r.Restricted {
		return
	}
	update := s.update
	ctx := s.viewContext(r)
	r.Life().Go(func(context.Context) {
		var rv forge.Review
		c, err := find(repo.Kind)
		if err == nil {
			rv, err = c.PullReview(ctx, repo, n)
		}
		if ctx.Err() != nil {
			return
		}
		update(func() {
			if ctx.Err() == nil {
				done(rv, err)
			}
		})
	})
}

// ForgeDo runs fn, a write to repo's host through its tool, off the main
// thread, and hands its error to done on it. It is not stopped by leaving
// the view: what the user sent is sent.
func (s *Session) ForgeDo(find func(forge.Kind) (*forge.CLI, error), repo forge.Repo, fn func(ctx context.Context, c *forge.CLI) error, done func(error)) {
	r := s.Repo
	if r == nil || r.Restricted {
		done(errors.New("session: the repository is not open"))
		return
	}
	update := s.update
	r.Life().Go(func(ctx context.Context) {
		c, err := find(repo.Kind)
		if err == nil {
			err = fn(ctx, c)
		}
		if ctx.Err() != nil {
			return
		}
		update(func() { done(err) })
	})
}

// hostPullRef is where repo's host publishes pull request n's head.
func hostPullRef(repo forge.Repo, n int) string {
	if repo.Kind == forge.GitLab {
		return "refs/merge-requests/" + strconv.Itoa(n) + "/head"
	}
	return "refs/pull/" + strconv.Itoa(n) + "/head"
}

// pullRemote is the remote whose URL names repo, else repo's URL.
func (s *Session) pullRemote(repo forge.Repo) string {
	cfg := s.Repo.Config()
	for _, name := range cfg.Subsections("remote", "url") {
		if u, ok := cfg.Get("remote." + name + ".url"); ok {
			if p, ok := forge.Parse(u); ok && p == repo {
				return name
			}
		}
	}
	return repo.Web() + ".git"
}

// FetchPull fetches pull request n of repo, whose base branch is base,
// into write.PullHead and write.PullBase, as a write in the queue.
func (s *Session) FetchPull(repo forge.Repo, n int, base string) (*write.Job, error) {
	if s.Repo == nil || s.Queue == nil {
		return nil, s.noWrites()
	}
	op, err := write.FetchPull(s.Where(), s.pullRemote(repo), n, hostPullRef(repo, n), base, 0)
	if err != nil {
		return nil, err
	}
	return s.Submit(op)
}

// PullRange is what a fetched pull request n changes: from the merge
// base of its head and base branch to its head, as the host diffs it.
type PullRange struct {
	MergeBase, Head []byte
}

// LoadPullRange reads the commits of pull request n, once fetched, off
// the main thread, and hands them to done on it.
func (s *Session) LoadPullRange(n int, done func(PullRange, error)) {
	r := s.Repo
	if r == nil {
		return
	}
	update := s.update
	r.Life().Go(func(ctx context.Context) {
		var pr PullRange
		out := func(args ...string) ([]byte, error) {
			res, err := r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, Read: true, Untranslated: true, Args: args})
			if err != nil {
				return nil, err
			}
			return hex.DecodeString(strings.TrimSpace(string(res.Stdout)))
		}
		head, err := out("rev-parse", "--verify", "--end-of-options", write.PullHead(n)+"^{commit}")
		if err == nil {
			pr.Head = head
			// The head read, not the ref, which a fetch may have moved since.
			pr.MergeBase, err = out("merge-base", "--end-of-options", write.PullBase(n), hex.EncodeToString(head))
		}
		update(func() { done(pr, err) })
	})
}
