package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ramus/internal/git/repo"
	"ramus/internal/git/run"
	"ramus/internal/git/status"
)

// Bounds on the worktree dashboard: how many worktrees it reads, how
// many statuses run at once, and how much one may print.
const (
	MaxDashboardWorktrees = 64
	dashboardWorkers      = 2
	dashboardMaxOutput    = 64 << 20
)

// WorktreeSummary is what the dashboard shows of one worktree beyond
// what the list has: how many files changed and when it was last used.
type WorktreeSummary struct {
	Path string
	// Changes counts the changed, staged, untracked and conflicted
	// files; Err says why it was not read.
	Changes int
	Err     error
	// Active is when the worktree's index was last written, as by a
	// checkout, a stage or a commit there; zero when unknown.
	Active time.Time
}

// SummarizeWorktrees reads, off the main thread, the summary of each
// worktree but the missing ones, a few at a time, and hands each to
// done on the main thread as it comes. It returns a function that stops
// what is not read yet.
func (s *Session) SummarizeWorktrees(wts []repo.Worktree, done func(WorktreeSummary)) (stop func()) {
	r := s.Repo
	if r == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(r.Life().Context())
	update := s.update
	jobs := make(chan repo.Worktree)
	for range dashboardWorkers {
		r.Life().Go(func(context.Context) {
			for wt := range jobs {
				sum := summarize(ctx, r.Runner, r.Caps.HashSize, wt)
				if ctx.Err() != nil {
					return
				}
				update(func() { done(sum) })
			}
		})
	}
	r.Life().Go(func(context.Context) {
		defer close(jobs)
		for i, wt := range wts {
			if i >= MaxDashboardWorktrees {
				return
			}
			if wt.Prunable || wt.Bare {
				continue
			}
			select {
			case jobs <- wt:
			case <-ctx.Done():
				return
			}
		}
	})
	return cancel
}

// summarize counts what changed in one worktree, through git status run
// there, and reads when its index was last written.
func summarize(ctx context.Context, r *run.Runner, hashSize int, wt repo.Worktree) WorktreeSummary {
	sum := WorktreeSummary{Path: wt.Path}
	gitDir := worktreeGitDir(wt.Path)
	if fi, err := os.Stat(filepath.Join(gitDir, "index")); err == nil {
		sum.Active = fi.ModTime()
	}
	// Discover: git finds the worktree's repository itself, with its
	// ownership checks, as in a terminal there.
	res, err := r.Run(ctx, &run.Cmd{GitDir: gitDir, WorkTree: wt.Path, Discover: true, Read: true, MaxOutput: dashboardMaxOutput,
		Args: []string{"status", "--porcelain=v2", "-z", "--no-ahead-behind", "--ignore-submodules=dirty"}})
	if err != nil {
		sum.Err = err
		return sum
	}
	st, err := status.Parse(res.Stdout, hashSize)
	if err != nil {
		sum.Err = err
		return sum
	}
	sum.Changes = st.Len()
	return sum
}

// worktreeGitDir is the git directory of the worktree at path: .git
// itself, or the folder its .git file names.
func worktreeGitDir(path string) string {
	dot := filepath.Join(path, ".git")
	fi, err := os.Lstat(dot)
	if err != nil || fi.IsDir() || fi.Size() > 4096 {
		return dot
	}
	b, err := os.ReadFile(dot)
	if err != nil {
		return dot
	}
	dir, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
	if !ok {
		return dot
	}
	dir = strings.TrimSpace(dir)
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(path, dir)
	}
	return dir
}
