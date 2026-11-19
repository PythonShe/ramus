package session

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"ramus/internal/forge"
	"ramus/internal/git/repo"
	"ramus/internal/git/run"
	"ramus/internal/git/write"
	"ramus/locales"
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
	// Partial says the details of some of List are still being read
	// (forge.Listed.Detailed): the list follows again, with more of them.
	Partial bool
	Err     error
	// DetailErr is why some of List stay without their details.
	DetailErr error
}

// OpenPulls lists the open pull requests of ReviewRepo, or of the
// repository it was forked from, off the main thread, through the tool
// find gives, and hands them to done on it: first as the host lists
// them, then again as their details are read, each time a list of its
// own, until one that is not Partial. Leaving the view drops the
// answers.
func (s *Session) OpenPulls(find func(forge.Kind) (*forge.CLI, error), done func(OpenPullsResult)) {
	r := s.Repo
	repo, ok := s.ReviewRepo()
	if !ok {
		done(OpenPullsResult{Err: forge.ErrNoReview})
		return
	}
	up, _ := RemoteURLs(r)
	_, named := forge.Parse(up)
	ctx := s.viewContext(r)
	// Only a trusted repository asks its hosts, and one never opens again.
	update := func(fn func()) { s.handOver(r, fn, nil) }
	r.Life().Go(func(context.Context) {
		res := OpenPullsResult{Repo: repo}
		c, err := find(repo.Kind)
		if err == nil {
			if !named {
				res.Repo, err = s.forks.parentOf(ctx, c, repo)
			}
			if err == nil {
				res.List, res.More, err = c.OpenPulls(ctx, res.Repo)
			}
			if err == nil && slices.ContainsFunc(res.List, func(l forge.Listed) bool { return !l.Detailed }) {
				part := func() {
					p := res
					p.List, p.Partial = slices.Clone(res.List), true
					update(func() {
						if ctx.Err() == nil {
							done(p)
						}
					})
				}
				part()
				res.DetailErr = c.PullDetails(ctx, res.Repo, res.List, part)
			}
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
	switch {
	case r == nil:
		done(forge.Review{}, errNotOpen)
		return
	case r.Restricted:
		done(forge.Review{}, ErrReadOnly)
		return
	}
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
		// Only a trusted repository asks its hosts, and one never opens again.
		s.handOver(r, func() {
			if ctx.Err() == nil {
				done(rv, err)
			}
		}, nil)
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
	r.Life().Go(func(ctx context.Context) {
		c, err := find(repo.Kind)
		if err == nil {
			err = fn(ctx, c)
		}
		if ctx.Err() != nil {
			return
		}
		// Only a trusted repository asks its hosts, and one never opens again.
		s.handOver(r, func() { done(err) }, nil)
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
// into write.PullHead and write.PullBase, as a write in the queue;
// deepen, above 0, that many commits deeper in a shallow clone. The
// session keeps them from ForgetPulls of every session until its own
// ForgetPulls leaves n out.
func (s *Session) FetchPull(repo forge.Repo, n int, base string, deepen int) (*write.Job, error) {
	if s.Repo == nil || s.Queue == nil {
		return nil, s.noWrites()
	}
	op, err := write.FetchPull(s.Where(), s.pullRemote(repo), n, hostPullRef(repo, n), base, deepen)
	if err != nil {
		return nil, err
	}
	s.usePulls(s.Repo.CommonDir, []int{n}, true)
	return s.Submit(op)
}

// ForgetPulls deletes the refs FetchPull kept of the pull requests not
// in keep, as a quiet write in the queue: they keep each pull request's
// commits from gc, its heads force-pushed over included, and a mirror
// push sends them. It is called with the open ones and the one under
// review, so they stay as many as the pull requests open. The pull
// requests another session of the repository keeps, as another window
// reviews, are kept too; a ref fetched again since it was read is left
// for the next call.
func (s *Session) ForgetPulls(keep []int) {
	r := s.Repo
	if r == nil || r.Restricted || s.Queue == nil {
		return
	}
	s.usePulls(r.CommonDir, keep, false)
	w := s.Where()
	rd := r.Refs()
	if rd == nil {
		return // refs not read here: the engine gave them to git
	}
	type gone struct {
		n         int
		name, old string
	}
	r.Life().Go(func(ctx context.Context) {
		list, err := rd.List()
		if err != nil {
			return
		}
		var drop []gone
		i, _ := list.Find(write.PullPrefix)
		for ; i < list.Len(); i++ {
			name := list.Name(i)
			rest, ok := strings.CutPrefix(name, write.PullPrefix)
			if !ok {
				break
			}
			num, _, _ := strings.Cut(rest, "/")
			n, err := strconv.Atoi(num)
			if err == nil && slices.Contains(keep, n) {
				continue
			}
			drop = append(drop, gone{n, name, hex.EncodeToString(list.OID(i))})
		}
		if len(drop) == 0 || ctx.Err() != nil {
			return
		}
		// Only a trusted repository asks its hosts, and one never opens
		// again.
		s.handOver(r, func() {
			if s.Queue == nil {
				return
			}
			// What the other sessions keep as the write is asked for: one
			// may have fetched a pull request since the refs were read.
			others := s.othersPulls(r.CommonDir)
			var in strings.Builder
			for _, g := range drop {
				if !slices.Contains(others, g.n) {
					// With the value read: a ref fetched again since is
					// not deleted.
					in.WriteString("delete " + g.name + " " + g.old + "\n")
				}
			}
			if in.Len() == 0 {
				return
			}
			op := write.Op{Name: locales.SessionForgetPulls(), RefsOnly: true, NoIndex: true, Input: []byte(in.String()),
				// No one waits on it: a quit may stop it, with loose refs
				// part way, some deleted and some not (on Windows a quit
				// lets it end). Every one was to go, and what it left is
				// deleted at the next call.
				Cmd: run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: []string{"update-ref", "--stdin"}, Background: true, EndsWithApp: true}}
			s.submit(op, repo.RefsChanged, true)
		}, nil)
	})
}

// PullRange is what a fetched pull request n changes: from the merge
// base of its head and base branch to its head, as the host diffs it.
type PullRange struct {
	MergeBase, Head []byte
}

// NoMergeBaseError is two commits, as a pull request's head and its base
// branch, that share none here: in a shallow clone (Shallow), the
// history fetched may not go down to it.
type NoMergeBaseError struct {
	Shallow bool
}

func (e *NoMergeBaseError) Error() string {
	if e.Shallow {
		return "session: no merge base in the shallow history fetched"
	}
	return "session: the commits share no history"
}

// mergeBase is the commit git diff a...b diffs from: git merge-base a b,
// which picks the same one of several. None is a NoMergeBaseError.
func mergeBase(ctx context.Context, r *repo.Repository, a, b string) ([]byte, error) {
	res, err := r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, Read: true, Untranslated: true,
		Args: []string{"merge-base", "--end-of-options", a, b}})
	var e *run.Error
	if errors.As(err, &e) && e.ExitCode == 1 {
		// git says none with 1 and nothing printed.
		_, statErr := os.Stat(filepath.Join(r.CommonDir, "shallow"))
		return nil, &NoMergeBaseError{Shallow: statErr == nil}
	}
	if err != nil {
		return nil, err
	}
	return hex.DecodeString(strings.TrimSpace(string(res.Stdout)))
}

// MergeBase reads the commit a branch at b split off from one at a, what
// git diff a...b diffs from, off the main thread, and hands it to done
// on it; NoMergeBaseError for none. Leaving the view stops the read, and
// done is not called; a read whose repository closed and opened again
// (Trust) is answered ErrReopened.
func (s *Session) MergeBase(a, b []byte, done func([]byte, error)) {
	r := s.Repo
	if r == nil {
		done(nil, errors.New("session: the repository is not open"))
		return
	}
	ctx := s.viewContext(r)
	shown := s.stillShown()
	r.Life().Go(func(context.Context) {
		mb, err := mergeBase(ctx, r, hex.EncodeToString(a), hex.EncodeToString(b))
		if ctx.Err() != nil && !closedUnder(r) {
			return
		}
		s.handOver(r, func() {
			if ctx.Err() == nil {
				done(mb, err)
			}
		}, func() {
			if shown() {
				done(nil, ErrReopened)
			}
		})
	})
}

// LoadPullRange reads the commits of pull request n, once fetched, off
// the main thread, and hands them to done on it; ErrReopened when the
// repository closed and opened again (Trust).
func (s *Session) LoadPullRange(n int, done func(PullRange, error)) {
	r := s.Repo
	if r == nil {
		done(PullRange{}, errNotOpen)
		return
	}
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
			pr.MergeBase, err = mergeBase(ctx, r, write.PullBase(n), hex.EncodeToString(head))
		}
		s.handOver(r, func() { done(pr, err) }, func() { done(PullRange{}, ErrReopened) })
	})
}
