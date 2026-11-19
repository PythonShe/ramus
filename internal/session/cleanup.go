package session

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"

	"ramus/internal/git/repo"
	"ramus/internal/git/run"
)

// Why says why a branch can go; a branch may have several reasons.
type Why uint8

const (
	// Merged is a branch the base contains.
	Merged Why = 1 << iota
	// RebaseMerged is one whose every commit is in the base as an equal
	// patch, as a rebase or cherry-pick leaves it.
	RebaseMerged
	// SquashMerged is one whose changes all came into the base as one
	// commit.
	SquashMerged
	// UpstreamGone is one whose upstream branch was deleted.
	UpstreamGone
)

// Candidate is a branch that can go, and why.
type Candidate struct {
	Branch BranchInfo
	Why    Why
}

// maxSquashScan bounds the base's commits searched for a squash of a
// branch; a branch further behind is not checked for one.
const maxSquashScan = 5000

// FindCleanup finds the branches that can go against base (a full ref
// name), leaving out current, off the main thread, and hands them to done
// on it. A search runs git a few times for each branch: a newer call
// cancels one still running, and so does cancel, for a search no longer
// shown; done is not called after either. A search whose repository
// closed and opened again (Trust) is answered ErrReopened.
func (s *Session) FindCleanup(base, current string, branches []BranchInfo, done func([]Candidate, error)) (cancel func()) {
	r := s.Repo
	if r == nil {
		done(nil, errors.New("session: the repository is not open"))
		return func() {}
	}
	l := &s.cleanup
	l.mu.Lock()
	if l.cancel != nil {
		l.cancel()
	}
	ctx, stop := context.WithCancel(r.Life().Context())
	l.gen++
	gen := l.gen
	l.cancel = stop
	l.mu.Unlock()
	// dropped is read and written on the main thread only.
	dropped := false
	live := func() bool {
		l.mu.Lock()
		defer l.mu.Unlock()
		return l.gen == gen && !dropped
	}
	r.Life().Go(func(context.Context) {
		defer stop()
		out, err := findCleanup(ctx, r, base, current, branches)
		if ctx.Err() != nil && !closedUnder(r) {
			return // cancelled, or a newer search took over
		}
		s.handOver(r, func() {
			if live() {
				done(out, err)
			}
		}, func() {
			if live() {
				done(nil, ErrReopened)
			}
		})
	})
	return func() { dropped = true; stop() }
}

func findCleanup(ctx context.Context, r *repo.Repository, base, current string, branches []BranchInfo) ([]Candidate, error) {
	gitOut := func(args ...string) (string, error) {
		res, err := r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, Read: true, MaxOutput: maxRefOutput, Args: args})
		return string(res.Stdout), err
	}
	merged := map[string]bool{}
	list, err := gitOut("for-each-ref", "--format=%(refname)", "--merged", base, "refs/heads/")
	if err != nil {
		return nil, err
	}
	for _, name := range strings.Fields(list) {
		merged[name] = true
	}
	// The patch ids of the base's commits since a merge base, by merge
	// base, read once each.
	baseIDs := map[string]map[string]bool{}
	var out []Candidate
	for _, b := range branches {
		if b.Name == base || b.Name == current {
			continue
		}
		var why Why
		if merged[b.Name] {
			why |= Merged
		}
		if b.Gone {
			why |= UpstreamGone
		}
		if why&Merged == 0 {
			cherry, err := gitOut("cherry", "--end-of-options", base, b.Name)
			if err != nil {
				return nil, err
			}
			lines := strings.Fields(cherry)
			if len(lines) > 0 && !strings.Contains(cherry, "+") {
				why |= RebaseMerged
			} else if squashed, err := isSquashed(ctx, r, gitOut, base, b.Name, baseIDs); err != nil {
				return nil, err
			} else if squashed {
				why |= SquashMerged
			}
		}
		if why != 0 {
			out = append(out, Candidate{Branch: b, Why: why})
		}
	}
	return out, nil
}

// isSquashed reports whether the branch's whole change since its merge
// base is, as one patch, a commit of the base since then.
func isSquashed(ctx context.Context, r *repo.Repository, gitOut func(...string) (string, error), base, branch string, baseIDs map[string]map[string]bool) (bool, error) {
	mb, err := gitOut("merge-base", "--end-of-options", base, branch)
	if err != nil {
		var e *run.Error
		if errors.As(err, &e) && e.ExitCode == 1 {
			return false, nil // no history in common
		}
		return false, err
	}
	mb = strings.TrimSpace(mb)
	ids, ok := baseIDs[mb]
	if !ok {
		count, err := gitOut("rev-list", "--count", "--no-merges", base, "^"+mb, "--")
		if err != nil {
			return false, err
		}
		if n, _ := strconv.Atoi(strings.TrimSpace(count)); n > maxSquashScan {
			baseIDs[mb] = nil
			return false, nil
		}
		ids, err = patchIDs(ctx, r, []string{"log", "-p", "--no-color", "--no-ext-diff", "--no-merges", "--format=commit %H", base, "^" + mb, "--"})
		if errors.Is(err, run.ErrOutputTooLarge) {
			baseIDs[mb] = nil
			return false, nil
		}
		if err != nil {
			return false, err
		}
		baseIDs[mb] = ids
	}
	if ids == nil {
		return false, nil
	}
	mine, err := patchIDs(ctx, r, []string{"diff", "--no-color", "--no-ext-diff", mb, branch, "--"})
	if errors.Is(err, run.ErrOutputTooLarge) {
		return false, nil
	}
	if err != nil || len(mine) != 1 {
		return false, err
	}
	for id := range mine {
		return ids[id], nil
	}
	return false, nil
}

// maxPatchInput bounds the patches patchIDs reads; past it a branch is
// not checked for a squash.
const maxPatchInput = 128 << 20

// patchIDs runs git with args and git patch-id --stable on what it
// prints, and returns the patch ids. One runs after the other: streamed,
// they would take two of the app's process slots, the first held while
// it waits for the second to read, and with the others busy each would
// wait on the other until the time limit.
func patchIDs(ctx context.Context, r *repo.Repository, args []string) (map[string]bool, error) {
	patches, err := r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, Read: true, Args: args, MaxOutput: maxPatchInput})
	if err != nil {
		return nil, err
	}
	res, err := r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, Read: true, Args: []string{"patch-id", "--stable"},
		Stdin: bytes.NewReader(patches.Stdout)})
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, line := range bytes.Split(res.Stdout, []byte{'\n'}) {
		if f := bytes.Fields(line); len(f) == 2 {
			out[string(f[0])] = true
		}
	}
	return out, nil
}
