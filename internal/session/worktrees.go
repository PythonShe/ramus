package session

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ramus/internal/git/diff"
	"ramus/internal/git/repo"
	"ramus/internal/git/run"
	"ramus/internal/git/status"
)

// Bounds on the worktree dashboard and panel: how many worktrees they
// read, how many statuses run at once, how much one may print, how many
// files one lists, and how much of the untracked ones is read to count
// their lines.
const (
	MaxDashboardWorktrees = 64
	dashboardWorkers      = 2
	dashboardMaxOutput    = 64 << 20
	MaxWorktreeFiles      = 1000
	maxCountedFile        = 1 << 20
	maxCountedBytes       = 16 << 20
)

// InUseFor is how long after a file in a worktree last changed it counts
// as in use: an agent writing there pauses between edits.
const InUseFor = 2 * time.Minute

// worktreeEvery is the least time between two readings of the worktrees
// a live panel shows; each also waits twice what the last one took.
const worktreeEvery = 2 * time.Second

// WorktreeSummary is what the dashboard and the panel show of one
// worktree beyond what the list has: what changed there against its
// HEAD and when it was last used.
type WorktreeSummary struct {
	Path string
	// Changes counts the changed, staged, untracked and conflicted
	// files; Err says why it was not read.
	Changes int
	Err     error
	// Added and Removed count the lines changed against HEAD, untracked
	// files counted whole.
	Added, Removed int
	// Files are the first MaxWorktreeFiles changed, in path order.
	Files []WorktreeFile
	// Active is when the worktree was last used: its index written, as
	// by a checkout, a stage or a commit, or a changed file saved; zero
	// when unknown.
	Active time.Time
	// InUse is a worktree another program works in (see InUse); Busy is
	// one doing so now, as git holding its index or a file just saved.
	InUse, Busy bool
}

// WorktreeFile is a file changed in a worktree against its HEAD.
type WorktreeFile struct {
	Path                       string
	Untracked, Deleted, Binary bool
	Added, Removed             int
	// Changed is when the file was last written, zero for one deleted.
	Changed time.Time
}

// SummarizeWorktrees reads, off the main thread, the summary of each
// worktree but the missing ones, a few at a time, and hands each to
// done on the main thread as it comes. It returns a function that stops
// what is not read yet.
func (s *Session) SummarizeWorktrees(wts []repo.Worktree, done func(WorktreeSummary)) (stop func()) {
	return s.summarizeWorktrees(wts, done, false)
}

// WatchWorktrees is SummarizeWorktrees reading them again and again
// until stopped, for a panel that shows them live: each round waits
// twice what the last took, at least worktreeEvery, so a slow status
// never runs back to back.
func (s *Session) WatchWorktrees(wts []repo.Worktree, done func(WorktreeSummary)) (stop func()) {
	return s.summarizeWorktrees(wts, done, true)
}

func (s *Session) summarizeWorktrees(wts []repo.Worktree, done func(WorktreeSummary), again bool) (stop func()) {
	r := s.Repo
	if r == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(r.Life().Context())
	update := s.update
	every := worktreeEvery
	if s.worktreeEvery > 0 {
		every = s.worktreeEvery
	}
	r.Life().Go(func(context.Context) {
		for {
			t0 := time.Now()
			summarizeAll(ctx, r, wts, func(sum WorktreeSummary) {
				update(func() {
					if ctx.Err() == nil {
						done(sum)
					}
				})
			})
			if !again {
				return
			}
			t := time.NewTimer(max(every, 2*time.Since(t0)))
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
		}
	})
	return cancel
}

// summarizeAll summarizes the worktrees but the missing and bare ones,
// a few at a time, handing each to emit as it comes, off the main
// thread; it returns once all are read or ctx ends.
func summarizeAll(ctx context.Context, r *repo.Repository, wts []repo.Worktree, emit func(WorktreeSummary)) {
	jobs := make(chan repo.Worktree)
	ended := make(chan struct{})
	for range dashboardWorkers {
		go func() {
			defer func() { ended <- struct{}{} }()
			for wt := range jobs {
				sum := summarize(ctx, r.Runner, r.Caps.HashSize, wt, time.Now())
				if ctx.Err() != nil {
					continue
				}
				emit(sum)
			}
		}()
	}
	n := 0
	for _, wt := range wts {
		if n >= MaxDashboardWorktrees {
			break
		}
		if wt.Prunable || wt.Bare {
			continue
		}
		n++
		select {
		case jobs <- wt:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(jobs)
	for range dashboardWorkers {
		<-ended
	}
}

// summarize reads what changed in one worktree against its HEAD,
// through git status and git diff run there, which take no lock, and
// when it was last used.
func summarize(ctx context.Context, r *run.Runner, hashSize int, wt repo.Worktree, now time.Time) (sum WorktreeSummary) {
	sum = WorktreeSummary{Path: wt.Path}
	gitDir := worktreeGitDir(wt.Path)
	if fi, err := os.Stat(filepath.Join(gitDir, "index")); err == nil {
		sum.Active = fi.ModTime()
	}
	// A lock counts while it is fresh: one a crashed git left stays for
	// good, and would keep the worktree busy, and the fetch waiting, for
	// ever.
	lock, lockErr := os.Lstat(filepath.Join(gitDir, "index.lock"))
	locked := lockErr == nil && now.Sub(lock.ModTime()) < InUseFor
	defer func() {
		sum.Busy = locked || !sum.Active.IsZero() && now.Sub(sum.Active) < InUseFor
		sum.InUse = sum.Busy || wt.Locked || repo.InProgress(gitDir) != repo.NoOperation
	}()
	// Discover: git finds the worktree's repository itself, with its
	// ownership checks, as in a terminal there. Every untracked file is
	// listed, not the folders they are in: an agent makes new ones.
	res, err := r.Run(ctx, &run.Cmd{GitDir: gitDir, WorkTree: wt.Path, Discover: true, Read: true, MaxOutput: dashboardMaxOutput,
		Args: []string{"status", "--porcelain=v2", "-z", "--no-ahead-behind", "--ignore-submodules=dirty", "--untracked-files=all"}})
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
	var lines *diff.NumStat
	if wt.Head != "" {
		res, err := r.Run(ctx, &run.Cmd{GitDir: gitDir, WorkTree: wt.Path, Discover: true, Read: true, MaxOutput: dashboardMaxOutput,
			Config: []string{"core.quotePath=false"},
			// diff-index, not diff, which refreshes the index (diff.Head).
			Args: []string{"diff-index", "--numstat", "-z", "--no-renames", "--ignore-submodules=dirty", "HEAD"}})
		if err == nil {
			lines, _ = diff.ParseNumStat(res.Stdout)
		}
	}
	budget := maxCountedBytes
	for i := range st.Len() {
		f := WorktreeFile{Path: string(st.Path(i)), Untracked: st.Kind(i) == status.Untracked}
		x, y := st.XY(i)
		f.Deleted = x == 'D' || y == 'D'
		if l := lines.Find(st.Path(i), st.Path(i)); l.Known {
			f.Added, f.Removed, f.Binary = int(l.Added), int(l.Deleted), l.Binary
		}
		file := filepath.Join(wt.Path, filepath.FromSlash(f.Path))
		if !f.Deleted {
			if fi, err := os.Lstat(file); err == nil {
				f.Changed = fi.ModTime()
				if f.Changed.After(sum.Active) {
					sum.Active = f.Changed
				}
				if f.Untracked && fi.Mode().IsRegular() && fi.Size() <= maxCountedFile && int(fi.Size()) <= budget {
					budget -= int(fi.Size())
					f.Added, f.Binary = countLines(file)
				}
			}
		}
		if f.Binary {
			f.Added, f.Removed = 0, 0
		}
		sum.Added += f.Added
		sum.Removed += f.Removed
		if len(sum.Files) < MaxWorktreeFiles {
			sum.Files = append(sum.Files, f)
		}
	}
	return sum
}

// countLines counts the lines of a file that is not binary, as git
// would show it added whole: a NUL in its start makes it binary.
func countLines(file string) (n int, binary bool) {
	f, err := os.Open(file)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxCountedFile))
	if err != nil {
		return 0, false
	}
	l := diff.CountText(data)
	return int(l.Added), l.Binary
}

// WorktreePatch reads, off the main thread, the patch of one file in a
// worktree against its HEAD, or added whole when untracked, and hands
// it to done on the main thread. It returns a function that stops it.
func (s *Session) WorktreePatch(worktree, path string, untracked bool, done func(PatchResult)) (stop func()) {
	r := s.Repo
	if r == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(r.Life().Context())
	update := s.update
	gitDir := worktreeGitDir(worktree)
	r.Life().Go(func(context.Context) {
		req := diff.PatchRequest{Source: diff.Head, Paths: []string{path}, Context: -1, MaxBytes: 16 << 20}
		if untracked {
			req.Source = diff.Untracked
		}
		var res PatchResult
		p, err := diff.WorkTreePatch(ctx, r.Runner, gitDir, worktree, req)
		res.Truncated = p.Truncated
		if err == nil {
			res.Parsed, err = diff.Parse(p.Text)
		}
		res.Err = err
		update(func() {
			if ctx.Err() == nil {
				done(res)
			}
		})
	})
	return cancel
}

// othersBusy reports whether a worktree of r but the one it was opened
// in is busy (WorktreeSummary.Busy), off the main thread: a fetch then
// would race the git working there for the refs.
func othersBusy(ctx context.Context, r *repo.Repository) bool {
	wts, err := r.Worktrees(ctx)
	if err != nil {
		return false
	}
	var others []repo.Worktree
	for _, w := range wts {
		if !w.Current {
			others = append(others, w)
		}
	}
	if len(others) == 0 {
		return false
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	busy := make(chan bool, len(others))
	summarizeAll(ctx, r, others, func(sum WorktreeSummary) { busy <- sum.Busy })
	close(busy)
	for b := range busy {
		if b {
			return true
		}
	}
	return false
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
