package session

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"ramus/internal/git/run"
	"ramus/internal/git/write"
)

// EditCommit is a commit of a run of history being edited.
type EditCommit struct {
	OID     []byte
	Subject string
	Author  string
	// Branches are the local branches, by full name, at this commit,
	// HEAD's own left out: it moves with the rewrite anyway.
	Branches []string
}

// MaxEdit bounds the commits one edit of history replays.
const MaxEdit = 5000

// ErrMergeInRange is a run of history with a merge in it, which an edit
// written as a whole todo does not replay.
var ErrMergeInRange = errors.New("the commits from here to HEAD include a merge")

// EditRange reads the commits from from (inclusive) to HEAD along its
// first parents, oldest first, for an edit of that history, off the main
// thread, and hands them to done on it with the rewrite they make.
func (s *Session) EditRange(from []byte, done func(rw write.Rewrite, commits []EditCommit, err error)) {
	r := s.Repo
	if r == nil {
		return
	}
	seen := s.Seen()
	if seen.Head == "" {
		done(write.Rewrite{}, nil, errors.New("there are no commits yet"))
		return
	}
	tips := map[string][]string{}
	if l := s.State.Refs; l != nil {
		for i := range l.Len() {
			if n := l.Name(i); strings.HasPrefix(n, "refs/heads/") && n != seen.Branch {
				tips[string(l.OID(i))] = append(tips[string(l.OID(i))], n)
			}
		}
	}
	rw := write.Rewrite{Commit: hex.EncodeToString(from), Git: r.Runner.Git().Version, Branches: s.LocalBranches()}
	update := s.update
	s.goView(r, func(ctx context.Context) {
		commits, parent, err := readEditRange(ctx, r.Runner, r.GitDir, seen.Head, from)
		for i := range commits {
			commits[i].Branches = tips[string(commits[i].OID)]
		}
		rw.Parent = parent
		if ctx.Err() == nil {
			update(func() { done(rw, commits, err) })
		}
	})
}

// readEditRange lists the commits from from to head along first parents,
// oldest first, and from's parent, "" for a root commit.
func readEditRange(ctx context.Context, rn *run.Runner, gitDir, head string, from []byte) ([]EditCommit, string, error) {
	res, err := rn.Run(ctx, &run.Cmd{GitDir: gitDir, Read: true, MaxOutput: 64 << 20,
		Args: []string{"log", "--first-parent", "--format=%H%x1f%P%x1f%an%x1f%s%x00",
			"--max-count=" + strconv.Itoa(MaxEdit+1), "--end-of-options", head}})
	if err != nil {
		return nil, "", err
	}
	var out []EditCommit
	parent, found := "", false
	for rec := range bytes.SplitSeq(res.Stdout, []byte{0}) {
		f := bytes.SplitN(bytes.TrimLeft(rec, "\n"), []byte{0x1f}, 4)
		if len(f) != 4 {
			continue
		}
		id, err := hex.DecodeString(string(f[0]))
		if err != nil {
			continue
		}
		parents := bytes.Fields(f[1])
		if len(parents) > 1 {
			return nil, "", ErrMergeInRange
		}
		out = append(out, EditCommit{OID: id, Author: string(f[2]), Subject: string(f[3])})
		if bytes.Equal(id, from) {
			if len(parents) == 1 {
				parent = string(parents[0])
			}
			found = true
			break
		}
	}
	if !found {
		if len(out) > MaxEdit {
			return nil, "", fmt.Errorf("more than %d commits from here to HEAD", MaxEdit)
		}
		return nil, "", errors.New("this commit is not on HEAD's line of first parents")
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, parent, nil
}
