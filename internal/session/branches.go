package session

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"

	"ramus/internal/forge"
	"ramus/internal/git/repo"
	"ramus/internal/git/run"
	"ramus/internal/git/stack"
	"ramus/internal/git/write"
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
	// Signed is an annotated tag that carries a signature.
	Signed  bool
	Time    int64
	Subject string
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
	// Stacks are branches stacked on each other off the trunk, each from
	// the bottom branch to the top, by full ref name.
	Stacks [][]string
	// GitHubStacks are the stacks gh stack keeps for this worktree.
	GitHubStacks []forge.Stack
	Err          error
}

// StackOf returns the stack name is in whose top is name or, failing
// that, the first one it is in; nil when it is in none.
func (ri *RefInfo) StackOf(name string) []string {
	var in []string
	for _, s := range ri.Stacks {
		if s[len(s)-1] == name {
			return s
		}
		if in == nil && slices.Contains(s, name) {
			in = s
		}
	}
	return in
}

// trunkRefs are the refs whose history every stack starts from.
var trunkRefs = []string{"refs/remotes/origin/HEAD", "refs/heads/main", "refs/heads/master"}

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
		"--format=%(refname)%00%(objectname)%00%(objecttype)%00%(*objectname)%00%(upstream)%00%(upstream:track,nobracket)%00%(creatordate:unix)%00%(contents:subject)%00%(if)%(contents:signature)%(then)s%(end)",
		"refs/heads/", "refs/tags/", trunkRefs[0]}})
	if err != nil {
		return RefInfo{Err: err}
	}
	var trunk []string
	for _, line := range bytes.Split(res.Stdout, []byte{'\n'}) {
		f := strings.Split(string(line), "\x00")
		if len(f) != 9 {
			continue
		}
		if slices.Contains(trunkRefs, f[0]) {
			trunk = append(trunk, f[1])
		}
		when, _ := strconv.ParseInt(f[6], 10, 64)
		switch {
		case strings.HasPrefix(f[0], "refs/heads/"):
			b := BranchInfo{Name: f[0], Commit: f[1], Upstream: f[4], Time: when, Subject: f[7]}
			b.Ahead, b.Behind, b.Gone = parseTrack(f[5])
			info.Branches = append(info.Branches, b)
		case strings.HasPrefix(f[0], "refs/tags/"):
			t := TagInfo{Name: f[0], Object: f[1], Annotated: f[2] == "tag", Signed: f[2] == "tag" && f[8] == "s", Time: when, Subject: f[7]}
			switch {
			case f[2] == "commit":
				t.Commit = f[1]
			case f[3] != "":
				t.Commit = f[3]
			}
			info.Tags = append(info.Tags, t)
		}
	}
	var heads []stack.Branch
	for _, b := range info.Branches {
		heads = append(heads, stack.Branch{Name: b.Name, Tip: b.Commit})
	}
	// Stacks are a help; a repository too deep for them shows none.
	info.Stacks, _ = stack.Find(ctx, r.Runner, r.GitDir, heads, trunk)
	info.GitHubStacks, _ = forge.ReadStacks(r.GitDir)
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

// PushStack pushes the branches of stack, as RefInfo.Stacks lists them,
// in one push: to the remote the top branch tracks, else the one remote
// there is, else origin. Each goes to the branch it tracks there, or one
// of its own name.
func (s *Session) PushStack(stack []string) (*write.Job, error) {
	r := s.Repo
	if r == nil || len(stack) == 0 {
		return nil, errors.New("session: nothing to push")
	}
	cfg := r.Config()
	short := func(b string) string { return strings.TrimPrefix(b, "refs/heads/") }
	remote, _ := cfg.Get("branch." + short(stack[len(stack)-1]) + ".remote")
	if remote == "" || remote == "." {
		remotes := cfg.Subsections("remote", "url")
		switch {
		case len(remotes) == 1:
			remote = remotes[0]
		case slices.Contains(remotes, "origin"):
			remote = "origin"
		default:
			return nil, ErrNoRemote
		}
	}
	var branches []write.StackBranch
	for _, b := range stack {
		to := b
		if rm, _ := cfg.Get("branch." + short(b) + ".remote"); rm == remote {
			if m, _ := cfg.Get("branch." + short(b) + ".merge"); strings.HasPrefix(m, "refs/heads/") {
				to = m
			}
		}
		branches = append(branches, write.StackBranch{Local: b, Remote: to})
	}
	op, err := write.PushStack(s.Where(), remote, branches)
	if err != nil {
		return nil, err
	}
	return s.Submit(op)
}
