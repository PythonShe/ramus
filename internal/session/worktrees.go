package session

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ramus/internal/git/config"
	"ramus/internal/git/diff"
	"ramus/internal/git/pathsafe"
	"ramus/internal/git/repo"
	"ramus/internal/git/run"
	"ramus/internal/git/status"
	"ramus/internal/git/trust"
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
// what is not read yet. Each worktree not summarized when the repository
// closed and opened again (Trust) is answered with ErrReopened.
func (s *Session) SummarizeWorktrees(wts []repo.Worktree, done func(WorktreeSummary)) (stop func()) {
	return s.summarizeWorktrees(wts, done, false)
}

// WatchWorktrees is SummarizeWorktrees reading them again and again
// until stopped, for a panel that shows them live: each round waits
// twice what the last took, at least worktreeEvery, so a slow status
// never runs back to back. A watch the repository closing and opening
// again (Trust) ended answers ErrReopened for each worktree, whatever it
// told before: it is to be started again.
func (s *Session) WatchWorktrees(wts []repo.Worktree, done func(WorktreeSummary)) (stop func()) {
	return s.summarizeWorktrees(wts, done, true)
}

func (s *Session) summarizeWorktrees(wts []repo.Worktree, done func(WorktreeSummary), again bool) (stop func()) {
	r := s.Repo
	if r == nil {
		for _, wt := range wts {
			done(WorktreeSummary{Path: wt.Path, Err: errNotOpen})
		}
		return func() {}
	}
	ctx, cancel := context.WithCancel(r.Life().Context())
	in := s.gitIn(r)
	every := worktreeEvery
	if s.worktreeEvery > 0 {
		every = s.worktreeEvery
	}
	// Main thread only: stopped by the caller, and the worktrees
	// answered, by path.
	stopped, told := false, map[string]bool{}
	reopened := func() {
		if stopped {
			return
		}
		for _, wt := range summarized(wts) {
			// A watch, which ended with the repository whatever it told
			// before, says so of each, for the panel to watch again.
			if again || !told[wt.Path] {
				told[wt.Path] = true
				done(WorktreeSummary{Path: wt.Path, Err: ErrReopened})
			}
		}
	}
	r.Life().Go(func(context.Context) {
		defer func() {
			if closedUnder(r) {
				s.handOver(r, nil, reopened)
			}
		}()
		for {
			t0 := time.Now()
			summarizeAll(ctx, r, in, wts, true, func(sum WorktreeSummary) {
				s.handOver(r, func() {
					if ctx.Err() == nil {
						told[sum.Path] = true
						done(sum)
					}
				}, func() {
					// A one-shot read may have ended seeing r open, and
					// told nothing on its way out; a watch's end says so
					// of each worktree itself.
					if !again && !stopped && !told[sum.Path] {
						told[sum.Path] = true
						done(WorktreeSummary{Path: sum.Path, Err: ErrReopened})
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
	return func() { stopped = true; cancel() }
}

// summarized are the worktrees of wts summarizeAll summarizes.
func summarized(wts []repo.Worktree) []repo.Worktree {
	var out []repo.Worktree
	for _, wt := range wts {
		if len(out) >= MaxDashboardWorktrees {
			break
		}
		if !wt.Prunable && !wt.Bare {
			out = append(out, wt)
		}
	}
	return out
}

// summarizeAll summarizes the worktrees but the missing and bare ones,
// a few at a time, running git in each as in says, handing each to emit
// as it comes, off the main thread; it returns once all are read or ctx
// ends. count counts the lines changed, as summarize does.
func summarizeAll(ctx context.Context, r *repo.Repository, in gitIn, wts []repo.Worktree, count bool, emit func(WorktreeSummary)) {
	jobs := make(chan repo.Worktree)
	ended := make(chan struct{})
	for range dashboardWorkers {
		go func() {
			defer func() { ended <- struct{}{} }()
			for wt := range jobs {
				// A panic summarizing one is reported, and the worker
				// goes on, so the loop handing out the rest is not
				// left waiting.
				func() {
					defer r.Life().Recover()
					sum := summarize(ctx, in, r.Caps.HashSize, wt, count, time.Now())
					if ctx.Err() != nil {
						return
					}
					emit(sum)
				}()
			}
		}()
	}
	for _, wt := range summarized(wts) {
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
// when it was last used. Without count, the lines changed are not
// counted: Busy needs none of them, and counting reads the untracked
// files whole.
func summarize(ctx context.Context, in gitIn, hashSize int, wt repo.Worktree, count bool, now time.Time) (sum WorktreeSummary) {
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
	loc, r, err := in(ctx, wt.Path)
	if err != nil {
		sum.Err = err
		return sum
	}
	// Every untracked file is listed, not the folders they are in: an
	// agent makes new ones.
	res, err := r.Run(ctx, &run.Cmd{GitDir: loc.GitDir, WorkTree: loc.WorkTree, Read: true, MaxOutput: dashboardMaxOutput,
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
	if wt.Head != "" && count {
		res, err := r.Run(ctx, &run.Cmd{GitDir: loc.GitDir, WorkTree: loc.WorkTree, Read: true, MaxOutput: dashboardMaxOutput,
			Config: []string{"core.quotePath=false"},
			// diff-index, not diff, which refreshes the index (diff.Head).
			Args: []string{"diff-index", "--numstat", "-z", "--no-renames", "--ignore-submodules=dirty", "HEAD", "--"}})
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
				if count && f.Untracked && fi.Mode().IsRegular() && fi.Size() <= maxCountedFile && int(fi.Size()) <= budget {
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
	// The file may have become a named pipe since it was looked at.
	f, err := pathsafe.OpenRegular(file)
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
// it to done on the main thread. It returns a function that stops it. A
// read whose repository closed and opened again (Trust) is answered
// ErrReopened.
func (s *Session) WorktreePatch(worktree, path string, untracked bool, done func(PatchResult)) (stop func()) {
	r := s.Repo
	if r == nil {
		done(PatchResult{Err: errNotOpen})
		return func() {}
	}
	ctx, cancel := context.WithCancel(r.Life().Context())
	in := s.gitIn(r)
	stopped := false // main thread only
	r.Life().Go(func(context.Context) {
		req := diff.PatchRequest{Source: diff.Head, Paths: []string{path}, Context: -1, MaxBytes: 16 << 20}
		if untracked {
			req.Source = diff.Untracked
		}
		var res PatchResult
		loc, rn, err := in(ctx, worktree)
		if err == nil {
			var p diff.Patch
			p, err = diff.WorkTreePatch(ctx, rn, loc.GitDir, loc.WorkTree, req)
			res.Truncated = p.Truncated
			if err == nil {
				res.Parsed, err = diff.Parse(p.Text)
			}
		}
		res.Err = err
		if ctx.Err() != nil && !closedUnder(r) {
			return
		}
		s.handOver(r, func() {
			if ctx.Err() == nil {
				done(res)
			}
		}, func() {
			if !stopped {
				done(PatchResult{Err: ErrReopened})
			}
		})
	})
	return func() { stopped = true; cancel() }
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
	// Without the session's trust store, only the folders r's trust is
	// recorded under count as trusted, and those only when r is: not the
	// other worktrees of a git directory r was trusted apart from, by its
	// work tree.
	own := func(trustDir string) bool { return !r.Restricted && trustDir == r.TrustDir }
	in := func(ctx context.Context, dir string) (repo.Location, *run.Runner, error) {
		return folderGit(ctx, r, r.Runner, own, dir)
	}
	busy := make(chan bool, len(others))
	summarizeAll(ctx, r, in, others, false, func(sum WorktreeSummary) { busy <- sum.Busy })
	close(busy)
	for b := range busy {
		if b {
			return true
		}
	}
	return false
}

// gitIn finds the repository whose work tree is a folder and the runner
// for git there, as folderGit does.
type gitIn func(ctx context.Context, dir string) (repo.Location, *run.Runner, error)

// gitIn is folderGit for the folders r names, with the trust the user
// gave. Call it on the main thread.
func (s *Session) gitIn(r *repo.Repository) gitIn {
	plain, ts := r.Runner, s.trust
	if r.Restricted && s.runner != nil {
		plain = s.runner
	}
	trusted := func(trustDir string) bool { return ts != nil && ts.Trusted(trustDir) }
	return func(ctx context.Context, dir string) (repo.Location, *run.Runner, error) {
		return folderGit(ctx, r, plain, trusted, dir)
	}
}

// errNoRepository is a folder that is not the top of a work tree.
var errNoRepository = errors.New("session: the folder holds no repository of its own")

// folderGit finds the repository whose work tree is dir, a folder other
// than the one r was opened in (a worktree r lists, a submodule), and
// the runner for git there. r's own runner will not do: its hardening
// covers the drivers of r's config, not those of a linked worktree's
// config.worktree, and the folder r names may be another repository
// altogether, with a config of its own. git runs there through plain
// only when the user trusts the folder as repo.Open would ask it
// (repo.TrustDir); otherwise it runs hardened from the folder's own
// config, as r would were it not trusted.
func folderGit(ctx context.Context, r *repo.Repository, plain *run.Runner, trusted func(trustDir string) bool, dir string) (repo.Location, *run.Runner, error) {
	loc, err := repo.Discover(dir)
	if err != nil {
		return loc, nil, err
	}
	// Discovery walks up to the repository above when dir holds none,
	// and spells the work tree as the disk does.
	if real, err := pathsafe.Real(dir); err != nil || loc.WorkTree != real {
		return loc, nil, errNoRepository
	}
	// git is given the folders found here rather than finding its own,
	// so it checks no ownership itself.
	if err := repo.CheckOwnership(ctx, r.Runner, loc); err != nil {
		return loc, nil, err
	}
	// Most folders are told without their config: the one holding its
	// .git directory, a worktree git registered. A submodule's needs its
	// core.worktree, so the config is read only where that could decide.
	var cfg *config.Config
	dir = repo.TrustDir(loc, nil)
	if dir != loc.CommonDir {
		if cfg, err = config.Read(ctx, r.Runner, loc.GitDir); err != nil {
			return loc, nil, err
		}
		dir = repo.TrustDir(loc, cfg)
	}
	if trusted(dir) {
		return loc, plain, nil
	}
	if cfg == nil {
		if cfg, err = config.Read(ctx, r.Runner, loc.GitDir); err != nil {
			return loc, nil, err
		}
	}
	return loc, r.Runner.Hardened(trust.Hardening(cfg)), nil
}

// worktreeGitDir is the git directory of the worktree at path: .git
// itself, or the folder its .git file names.
func worktreeGitDir(path string) string {
	dot := filepath.Join(path, ".git")
	fi, err := os.Lstat(dot)
	if err != nil || fi.IsDir() || fi.Size() > 4096 {
		return dot
	}
	// Regular files only: the folder is any the repository names, and a
	// named pipe there would hold the read, and the repository's close,
	// for ever.
	b, err := pathsafe.ReadFile(dot, 4096)
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
