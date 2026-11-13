package session

import (
	"context"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strconv"
	"strings"

	"ramus/internal/git/repo"
	"ramus/internal/git/run"
)

// SubmoduleRange is what a submodule's change moves over: the commits
// the new commit has that the old one has not (Added), and the other way
// (Removed), the latest first.
type SubmoduleRange struct {
	Added, Removed []RangeCommit
	// More is set when there were more than maxRange either way.
	More bool
	Err  error
}

// RangeCommit is one commit of a range.
type RangeCommit struct {
	ID, Subject string
}

// maxRange bounds the commits listed each way.
const maxRange = 500

// ErrNotCheckedOut is a submodule with no repository in the work tree,
// whose commits cannot be read.
var ErrNotCheckedOut = errors.New("session: the submodule is not checked out")

// LoadSubmoduleRange lists the commits between a submodule's old and new
// commits, from its repository in the work tree, off the main thread,
// and hands them to done on it.
func (s *Session) LoadSubmoduleRange(path string, old, new []byte, done func(SubmoduleRange)) {
	r := s.Repo
	if r == nil || r.WorkTree == "" {
		done(SubmoduleRange{Err: ErrNotCheckedOut})
		return
	}
	update := s.update
	r.Life().Go(func(ctx context.Context) {
		res := submoduleRange(ctx, r, path, old, new)
		if ctx.Err() == nil {
			update(func() { done(res) })
		}
	})
}

func submoduleRange(ctx context.Context, r *repo.Repository, path string, old, new []byte) SubmoduleRange {
	dir := filepath.Join(r.WorkTree, filepath.FromSlash(path))
	loc, err := repo.Discover(dir)
	if err != nil {
		return SubmoduleRange{Err: err}
	}
	// Discovery walks up to the parent when the submodule is not there.
	if real, err := filepath.EvalSymlinks(dir); err != nil || loc.WorkTree != real {
		return SubmoduleRange{Err: ErrNotCheckedOut}
	}
	if err := repo.CheckOwnership(ctx, r.Runner, loc); err != nil {
		return SubmoduleRange{Err: err}
	}
	list := func(from, to []byte) ([]RangeCommit, bool, error) {
		res, err := r.Runner.Run(ctx, &run.Cmd{GitDir: loc.GitDir, Read: true, Args: []string{"log", "--no-decorate",
			"--format=%H%x00%s", "--max-count=" + strconv.Itoa(maxRange+1), hex.EncodeToString(to), "^" + hex.EncodeToString(from), "--"}})
		if err != nil {
			return nil, false, err
		}
		var out []RangeCommit
		for _, line := range strings.Split(strings.TrimRight(string(res.Stdout), "\n"), "\n") {
			id, subject, ok := strings.Cut(line, "\x00")
			if ok {
				out = append(out, RangeCommit{ID: id, Subject: subject})
			}
		}
		more := len(out) > maxRange
		return out[:min(len(out), maxRange)], more, nil
	}
	var res SubmoduleRange
	var more bool
	if res.Added, more, err = list(old, new); err != nil {
		return SubmoduleRange{Err: err}
	}
	res.More = more
	if res.Removed, more, err = list(new, old); err != nil {
		return SubmoduleRange{Err: err}
	}
	res.More = res.More || more
	return res
}
