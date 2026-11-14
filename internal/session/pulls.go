package session

import (
	"context"
	"errors"
	"strings"

	"ramus/internal/forge"
)

// PullInfo is a branch's open pull request, and the repository it is in.
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

// LoadPulls asks the hosts, off the main thread, through the tool find
// gives for each (called off the main thread too), about the open pull requests of the branches of info
// that track a remote branch, and hands the answer to done on it. A
// repository whose tool is missing or not logged in is skipped, with
// that error.
func (s *Session) LoadPulls(find func(forge.Kind) (*forge.CLI, error), info *RefInfo, done func(PullsResult)) {
	r := s.Repo
	if r == nil || info == nil {
		done(PullsResult{Err: errors.New("session: the repository is not open")})
		return
	}
	type ask struct {
		local, remote []string
	}
	asks := map[forge.Repo]*ask{}
	var order []forge.Repo
	for _, b := range info.Branches {
		if b.Upstream == "" || b.Gone {
			continue
		}
		repo, name, ok := BranchRepo(b, info.Remotes)
		if !ok {
			continue
		}
		a := asks[repo]
		if a == nil {
			a = &ask{}
			asks[repo] = a
			order = append(order, repo)
		}
		a.local, a.remote = append(a.local, b.Name), append(a.remote, name)
	}
	ctx := s.viewContext(r)
	update := s.update
	r.Life().Go(func(context.Context) {
		res := PullsResult{Pulls: map[string]PullInfo{}}
		clis := map[forge.Kind]*forge.CLI{}
		for _, repo := range order {
			c, ok := clis[repo.Kind]
			if !ok {
				var err error
				// find looks on the PATH: off the main thread.
				if c, err = find(repo.Kind); err != nil && res.Err == nil {
					res.Err = err
				}
				clis[repo.Kind] = c
			}
			if c == nil {
				continue
			}
			a := asks[repo]
			got, err := c.Pulls(ctx, repo, a.remote)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				if res.Err == nil {
					res.Err = err
				}
				continue
			}
			for i, remote := range a.remote {
				if p, ok := got[remote]; ok {
					res.Pulls[a.local[i]] = PullInfo{Pull: p, Repo: repo}
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
