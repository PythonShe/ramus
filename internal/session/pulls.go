package session

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"time"

	"ramus/internal/forge"
	"ramus/internal/git/repo"
	"ramus/internal/git/write"
)

// PullInfo is a branch's pull request, and the repository it is in.
type PullInfo struct {
	forge.Pull
	Repo forge.Repo
}

// PullsResult is what the hosts said of the branches' pull requests, by
// full local branch name. Err is the first failure; the repositories
// that answered are in Pulls all the same.
type PullsResult struct {
	Pulls map[string]PullInfo
	Err   error
}

// BranchRepo is the forge repository branch b's pull requests are in, and
// the name the remote gives it there: its upstream's, or for a branch
// without one, origin's (or the only remote's) under its own name.
func BranchRepo(b BranchInfo, remotes []RemoteInfo) (forge.Repo, string, bool) {
	var rm *RemoteInfo
	name := strings.TrimPrefix(b.Name, "refs/heads/")
	if b.Upstream != "" {
		// A remote's name may hold a slash: the longest that fits wins.
		for i := range remotes {
			p := "refs/remotes/" + remotes[i].Name + "/"
			if strings.HasPrefix(b.Upstream, p) && (rm == nil || len(remotes[i].Name) > len(rm.Name)) {
				rm = &remotes[i]
			}
		}
		if rm == nil {
			return forge.Repo{}, "", false
		}
		name = strings.TrimPrefix(b.Upstream, "refs/remotes/"+rm.Name+"/")
	} else {
		for i := range remotes {
			if remotes[i].Name == "origin" || len(remotes) == 1 {
				rm = &remotes[i]
			}
		}
		if rm == nil {
			return forge.Repo{}, "", false
		}
	}
	u := rm.PushURL
	if u == "" {
		u = rm.URL
	}
	r, ok := forge.Parse(u)
	return r, name, ok
}

// ParentRemote is the name a fork's clone gives the repository it was
// forked from, by the common convention: its pull requests and issues
// are there.
const ParentRemote = "upstream"

// PullsRepo is the repository the pull requests from head's branches go
// to: the remote named upstream, when it is on the same host, else head
// itself.
func PullsRepo(head forge.Repo, remotes []RemoteInfo) forge.Repo {
	for _, rm := range remotes {
		if rm.Name != ParentRemote {
			continue
		}
		// Its fetch URL: a fork's clone often turns upstream's push off.
		if up, ok := forge.Parse(rm.URL); ok && up.Kind == head.Kind && up.Host == head.Host {
			return up
		}
	}
	return head
}

// RemoteURLs are the URLs of a repository's remote named upstream, ""
// when it has none, and of origin, or of the first remote when there is
// no origin.
func RemoteURLs(r *repo.Repository) (upstream, origin string) {
	cfg := r.Config()
	upstream, _ = cfg.Get("remote." + ParentRemote + ".url")
	if u, ok := cfg.Get("remote.origin.url"); ok {
		return upstream, u
	}
	for _, name := range cfg.Subsections("remote", "url") {
		if u, ok := cfg.Get("remote." + name + ".url"); ok {
			return upstream, u
		}
	}
	return upstream, ""
}

// LoadPulls asks the hosts, off the main thread, through the tool find
// gives for each (called off the main thread too), about the pull
// requests of the branches of info that track a remote branch, and hands
// the answer to done on it. A fork's branches are asked about in the
// repository it was forked from (PullsRepo). A repository whose tool is
// missing or not logged in is skipped, with that error; one on a host the
// tool does not know is skipped without one. A repository not trusted
// asks nothing: its remotes name the hosts.
func (s *Session) LoadPulls(find func(forge.Kind) (*forge.CLI, error), info *RefInfo, done func(PullsResult)) {
	r := s.Repo
	if r == nil || info == nil {
		done(PullsResult{Err: errors.New("session: the repository is not open")})
		return
	}
	if r.Restricted {
		done(PullsResult{})
		return
	}
	type where struct{ base, head forge.Repo }
	type ask struct {
		local, remote []string
	}
	asks := map[where]*ask{}
	var order []where
	for _, b := range info.Branches {
		if b.Upstream == "" || b.Gone {
			continue
		}
		head, name, ok := BranchRepo(b, info.Remotes)
		if !ok {
			continue
		}
		w := where{PullsRepo(head, info.Remotes), head}
		a := asks[w]
		if a == nil {
			a = &ask{}
			asks[w] = a
			order = append(order, w)
		}
		a.local, a.remote = append(a.local, b.Name), append(a.remote, name)
	}
	ctx := s.viewContext(r)
	update := s.update
	r.Life().Go(func(context.Context) {
		res := PullsResult{Pulls: map[string]PullInfo{}}
		clis := map[forge.Kind]*forge.CLI{}
		for _, w := range order {
			c, ok := clis[w.base.Kind]
			if !ok {
				var err error
				// find looks on the PATH: off the main thread.
				if c, err = find(w.base.Kind); err != nil && res.Err == nil {
					res.Err = err
				}
				clis[w.base.Kind] = c
			}
			if c == nil {
				continue
			}
			a := asks[w]
			got, err := c.Pulls(ctx, w.base, w.head, a.remote)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				if res.Err == nil && !errors.Is(err, forge.ErrUnknownHost) {
					res.Err = err
				}
				continue
			}
			for i, remote := range a.remote {
				if p, ok := got[remote]; ok {
					res.Pulls[a.local[i]] = PullInfo{Pull: p, Repo: w.base}
				}
			}
		}
		update(func() {
			if ctx.Err() == nil {
				done(res)
			}
		})
	})
}

// CommitPullResult is the pull request a commit came in by, as GitHub
// says; Found is false when it names none, or was not asked.
type CommitPullResult struct {
	Pull  PullInfo
	Found bool
	Err   error
	at    time.Time
}

// CommitPullMaxAge is how long what GitHub said of a commit is kept: an
// open pull request may be merged since.
const CommitPullMaxAge = 5 * time.Minute

func commitPullKey(oid []byte) string { return "p\x00" + string(oid) }

// LoadCommitPull asks GitHub, off the main thread, through the gh find
// gives (called off the main thread too), for the pull request that
// brought commit oid in, whose subject is subject, and hands the answer
// to done on it: from the repository named upstream when there is one,
// for a fork's commits came in there, else from origin. A repository
// not on GitHub, on a host gh does not know, or not trusted, is not
// asked. The answer is kept per commit for CommitPullMaxAge. cancel stops
// the asking and drops its answer.
func (s *Session) LoadCommitPull(find func(forge.Kind) (*forge.CLI, error), oid []byte, subject string, done func(CommitPullResult)) (cancel func()) {
	r := s.Repo
	if r == nil {
		return func() {}
	}
	if r.Restricted {
		done(CommitPullResult{})
		return func() {}
	}
	key := commitPullKey(oid)
	if a, ok := s.answers().Get(key); ok && a.pull != nil && s.timeNow().Sub(a.pull.at) < CommitPullMaxAge {
		done(*a.pull)
		return func() {}
	}
	ctx, stop := context.WithCancel(s.viewContext(r))
	// dropped is read and written on the main thread only.
	dropped := false
	hint := forge.SquashHint(subject)
	sha := hex.EncodeToString(oid)
	r.Life().Go(func(context.Context) {
		defer stop()
		res := s.commitPull(ctx, r, find, sha, hint)
		if ctx.Err() != nil {
			return // cancelled, or the repository closed
		}
		s.update(func() {
			if res.Err == nil {
				res.at = s.timeNow()
				s.keep(r, key, answer{pull: &res})
			}
			if !dropped {
				done(res)
			}
		})
	})
	return func() { dropped = true; stop() }
}

func (s *Session) commitPull(ctx context.Context, r *repo.Repository, find func(forge.Kind) (*forge.CLI, error), sha string, hint int) CommitPullResult {
	up, origin := RemoteURLs(r)
	gh, ok := forge.Parse(up)
	if !ok || gh.Kind != forge.GitHub {
		gh, ok = forge.Parse(origin)
	}
	if !ok || gh.Kind != forge.GitHub {
		return CommitPullResult{}
	}
	c, err := find(forge.GitHub)
	if err != nil {
		return CommitPullResult{Err: err}
	}
	p, found, err := c.CommitPull(ctx, gh, sha, hint)
	if errors.Is(err, forge.ErrUnknownHost) {
		return CommitPullResult{}
	}
	if err != nil {
		return CommitPullResult{Err: err}
	}
	return CommitPullResult{Pull: PullInfo{Pull: p, Repo: gh}, Found: found}
}

// GitHubStack runs gh stack with args in the work tree, as a write that
// takes its turn in the queue; name is what the log calls it.
func (s *Session) GitHubStack(c *forge.CLI, name string, args ...string) (*write.Job, error) {
	if s.Repo == nil || s.Queue == nil {
		return nil, s.noWrites()
	}
	dir := s.Repo.WorkTree
	line := "gh stack " + strings.Join(args, " ")
	return s.Submit(write.External(s.Where(), name, line, func(ctx context.Context, stdout, stderr io.Writer) error {
		return c.Stack(ctx, dir, stdout, stderr, args...)
	}))
}
