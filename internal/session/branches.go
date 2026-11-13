package session

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"

	"ramus/internal/git/repo"
	"ramus/internal/git/run"
)

// BranchInfo is a local branch as the branches view lists it.
type BranchInfo struct {
	Name   string // full ref name
	Commit string // hex
	// Upstream is the remote branch it tracks, "" for none; Gone is set
	// when that is configured but no longer exists.
	Upstream      string
	Ahead, Behind int
	Gone          bool
	Time          int64 // commit time of its tip
	Subject       string
	// Worktree is the path of another worktree that has it checked out.
	Worktree string
}

// TagInfo is a tag as the tags view lists it.
type TagInfo struct {
	Name      string // full ref name
	Object    string // what the ref holds, hex
	Commit    string // the commit it names, hex; "" for a tag of something else
	Annotated bool
	Time      int64
	Subject   string
}

// RemoteInfo is a remote and where it is.
type RemoteInfo struct {
	Name, URL, PushURL string
}

// RefInfo is what the branches view shows.
type RefInfo struct {
	Branches []BranchInfo
	Tags     []TagInfo
	Remotes  []RemoteInfo
	Err      error
}

// refLoad cancels a read a newer one replaced.
type refLoad struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	gen    int
}

// maxRefOutput bounds what the listing of refs reads.
const maxRefOutput = 128 << 20

// LoadRefInfo reads the branches with their upstreams, the tags and the
// remotes off the main thread, and hands them to done on it. A newer call
// cancels one still running.
func (s *Session) LoadRefInfo(done func(RefInfo)) {
	r := s.Repo
	if r == nil {
		return
	}
	s.refInfo.mu.Lock()
	if s.refInfo.cancel != nil {
		s.refInfo.cancel()
	}
	ctx, cancel := context.WithCancel(r.Life().Context())
	s.refInfo.gen++
	gen := s.refInfo.gen
	s.refInfo.cancel = cancel
	s.refInfo.mu.Unlock()
	update := s.update
	wts := s.State.Worktrees
	r.Life().Go(func(context.Context) {
		defer cancel()
		info := readRefInfo(ctx, r)
		for i, b := range info.Branches {
			for _, w := range wts {
				if w.Branch == b.Name && !w.Current {
					info.Branches[i].Worktree = w.Path
				}
			}
		}
		if ctx.Err() != nil {
			return
		}
		update(func() {
			s.refInfo.mu.Lock()
			ok := s.refInfo.gen == gen
			s.refInfo.mu.Unlock()
			if ok {
				done(info)
			}
		})
	})
}

func readRefInfo(ctx context.Context, r *repo.Repository) RefInfo {
	var info RefInfo
	res, err := r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, Read: true, MaxOutput: maxRefOutput, Args: []string{"for-each-ref",
		"--format=%(refname)%00%(objectname)%00%(objecttype)%00%(*objectname)%00%(upstream)%00%(upstream:track,nobracket)%00%(creatordate:unix)%00%(contents:subject)",
		"refs/heads/", "refs/tags/"}})
	if err != nil {
		return RefInfo{Err: err}
	}
	for _, line := range bytes.Split(res.Stdout, []byte{'\n'}) {
		f := strings.Split(string(line), "\x00")
		if len(f) != 8 {
			continue
		}
		when, _ := strconv.ParseInt(f[6], 10, 64)
		switch {
		case strings.HasPrefix(f[0], "refs/heads/"):
			b := BranchInfo{Name: f[0], Commit: f[1], Upstream: f[4], Time: when, Subject: f[7]}
			b.Ahead, b.Behind, b.Gone = parseTrack(f[5])
			info.Branches = append(info.Branches, b)
		case strings.HasPrefix(f[0], "refs/tags/"):
			t := TagInfo{Name: f[0], Object: f[1], Annotated: f[2] == "tag", Time: when, Subject: f[7]}
			switch {
			case f[2] == "commit":
				t.Commit = f[1]
			case f[3] != "":
				t.Commit = f[3]
			}
			info.Tags = append(info.Tags, t)
		}
	}
	info.Remotes, info.Err = readRemotes(ctx, r)
	return info
}

// parseTrack reads %(upstream:track,nobracket): "ahead 2, behind 1",
// "gone", or "" when even.
func parseTrack(s string) (ahead, behind int, gone bool) {
	if s == "gone" {
		return 0, 0, true
	}
	for _, part := range strings.Split(s, ", ") {
		k, v, ok := strings.Cut(part, " ")
		if !ok {
			continue
		}
		n, _ := strconv.Atoi(v)
		switch k {
		case "ahead":
			ahead = n
		case "behind":
			behind = n
		}
	}
	return ahead, behind, false
}

// readRemotes lists the remotes from git's config, by name.
func readRemotes(ctx context.Context, r *repo.Repository) ([]RemoteInfo, error) {
	res, err := r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, Read: true,
		Args: []string{"config", "-z", "--get-regexp", `^remote\..*\.(url|pushurl)$`}})
	var e *run.Error
	if errors.As(err, &e) && e.ExitCode == 1 {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []RemoteInfo
	at := map[string]int{}
	for _, rec := range bytes.Split(res.Stdout, []byte{0}) {
		key, val, ok := strings.Cut(string(rec), "\n")
		if !ok {
			continue
		}
		rest, _ := strings.CutPrefix(key, "remote.")
		name, which, ok := cutLast(rest, ".")
		if !ok {
			continue
		}
		i, seen := at[name]
		if !seen {
			i = len(out)
			at[name] = i
			out = append(out, RemoteInfo{Name: name})
		}
		if which == "url" && out[i].URL == "" {
			out[i].URL = val
		} else if which == "pushurl" && out[i].PushURL == "" {
			out[i].PushURL = val
		}
	}
	return out, nil
}

func cutLast(s, sep string) (string, string, bool) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return "", "", false
	}
	return s[:i], s[i+len(sep):], true
}
