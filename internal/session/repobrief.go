package session

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"strings"
	"time"

	"ramus/internal/forge"
	"ramus/internal/git/repo"
	"ramus/internal/git/run"
)

// RepoBrief is what the repository picker shows of a repository it may
// not have open: who it belongs to on its host, whether it is a fork,
// and how its branch stands with the branch it tracks.
type RepoBrief struct {
	// Owner is the user or group its remote is under on its host, ""
	// for a repository whose remotes name no host.
	Owner string
	// Fork is a repository with a remote named upstream, forked from it.
	Fork bool
	// Worktree is a worktree added to a repository with a work tree of
	// its own (repo.Location.Added), where it is listed.
	Worktree bool
	// Gone is a folder that is no longer there.
	Gone bool
	// Ahead and Behind count the commits of the branch checked out and
	// of the branch it tracks the other lacks, as of the last fetch.
	Ahead, Behind int
	Err           error
}

// briefTimeout bounds reading one repository's brief: a repository on a
// drive gone to sleep must not hold up the rest.
const briefTimeout = 10 * time.Second

// ReadBrief reads the brief of the repository at path, off the main
// thread, from its config and refs and one git for-each-ref that takes
// no lock. trusted says whether the user trusts a repository by its
// common git directory; one not trusted is read through a hardened git.
func ReadBrief(ctx context.Context, runner *run.Runner, trusted func(commonDir string) bool, path string) (b RepoBrief) {
	ctx, cancel := context.WithTimeout(ctx, briefTimeout)
	defer cancel()
	// A folder gone from inside another repository, as a worktree an
	// agent made and took away, would be read as that repository.
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		b.Gone, b.Err = true, err
		return b
	}
	r, err := repo.Open(ctx, runner, path, func(loc repo.Location) bool {
		return trusted != nil && trusted(loc.CommonDir)
	})
	if err != nil {
		b.Err = err
		return b
	}
	defer func() {
		r.Close()
		<-r.Life().Done()
	}()
	b.Worktree = r.Added()
	upstream, origin := RemoteURLs(r)
	b.Fork = upstream != ""
	b.Owner = remoteOwner(origin)
	branch := briefBranch(ctx, r)
	if branch == "" {
		return b
	}
	res, err := r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, Read: true,
		Args: []string{"for-each-ref", "--format=%(upstream:track,nobracket)", branch}})
	if err != nil {
		b.Err = err
		return b
	}
	b.Ahead, b.Behind, _ = parseTrack(strings.TrimSpace(string(res.Stdout)))
	return b
}

// briefBranch is the branch HEAD is on, as refs/heads/main, "" when it
// is detached or cannot be read: from the refs read natively, or from
// git where the engine does not read them.
func briefBranch(ctx context.Context, r *repo.Repository) string {
	if rd := r.Refs(); rd != nil {
		head, err := rd.Head()
		if err != nil || head.Detached || head.Unborn {
			return ""
		}
		return head.Target
	}
	res, err := r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, Read: true, Args: []string{"symbolic-ref", "-q", "HEAD"}})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(res.Stdout))
}

// remoteOwner is the user or group a remote URL is under: the owner a
// forge names, or on another host the first part of the path.
func remoteOwner(url string) string {
	if fr, ok := forge.Parse(url); ok {
		return fr.Owner()
	}
	_, path, ok := forge.HostPath(url)
	if !ok {
		return ""
	}
	owner, rest, _ := strings.Cut(strings.Trim(path, "/"), "/")
	if rest == "" {
		return ""
	}
	return owner
}
