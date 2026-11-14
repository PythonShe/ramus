package repo

import (
	"context"
	"path/filepath"
	"strings"

	"ramus/internal/git/watch"
)

// Change says what a batch of file changes touched, so the app refreshes
// only that: status for the work tree and index, refs and history for
// refs, the banner for an operation in progress.
type Change uint16

const (
	WorkTreeChanged Change = 1 << iota
	IndexChanged
	RefsChanged
	OperationChanged
	ConfigChanged
	GraphChanged
	// Rescan means notices were lost: refresh everything.
	Rescan
)

// Notice is one batch of classified changes.
type Notice struct {
	What Change
	// Paths are the work-tree paths that changed, slash-separated from the
	// top of the work tree; empty with Rescan.
	Paths []string
	// Err is why a watcher stopped on its own, such as watch.ErrLimit:
	// changes it would have seen are not reported from then on.
	Err error
}

// classifyGit names what a change to a path in a git directory (relative
// to it) touched.
func classifyGit(rel string) Change {
	first, rest, _ := strings.Cut(rel, "/")
	switch {
	case strings.HasSuffix(rel, ".lock"):
		return 0 // the file it guards changes when the lock is taken over
	case rel == "HEAD" || rel == "packed-refs" || first == "refs" || first == "reftable":
		return RefsChanged
	case rel == "index" || rel == "sharedindex" || strings.HasPrefix(rel, "sharedindex."):
		return IndexChanged
	case rel == "MERGE_HEAD" || rel == "CHERRY_PICK_HEAD" || rel == "REVERT_HEAD" || rel == "BISECT_LOG" ||
		first == "rebase-merge" || first == "rebase-apply" || first == "sequencer":
		return OperationChanged | RefsChanged
	case rel == "config" || rel == "config.worktree":
		return ConfigChanged
	case rel == "objects/info/commit-graph" || strings.HasPrefix(rel, "objects/info/commit-graphs"):
		return GraphChanged
	case first == "worktrees":
		// Another work tree's HEAD and refs, and a work tree added, removed
		// or locked, the folder itself when the last one is pruned; not
		// its index.
		_, sub, _ := strings.Cut(rest, "/")
		if sub == "" || sub == "HEAD" || sub == "gitdir" || sub == "locked" || strings.HasPrefix(sub, "refs/") {
			return RefsChanged
		}
	}
	return 0
}

// watched is one watcher and what its root is to the repository.
type watched struct {
	w *watch.Watcher
	// gitRel is the git directory relative to the root ("." when the root
	// is the git directory, ".git" for a main work tree), empty when the
	// root holds no git directory.
	gitRel string
	// commonRel is the common directory relative to the root, when it is
	// under it and differs from the git directory.
	commonRel string
	workTree  bool
}

func under(root, p string) (string, bool) {
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// classify names what one path of a batch touched and, for a work tree
// path, returns it.
func (w *watched) classify(p string) (Change, string) {
	for _, dir := range []string{w.gitRel, w.commonRel} {
		if dir == "" {
			continue
		}
		if dir == "." {
			return classifyGit(p), ""
		}
		if p == dir {
			return Rescan, "" // the directory itself was replaced
		}
		if rest, ok := strings.CutPrefix(p, dir+"/"); ok {
			return classifyGit(rest), ""
		}
	}
	if w.workTree {
		return WorkTreeChanged, p
	}
	return 0, ""
}

// Watch reports classified changes to the repository until it closes:
// one watcher on the work tree, and others on the git and common
// directories when they are not inside it (linked worktrees, submodules,
// a separate git dir). skipDir names work-tree directories not worth a
// watch where watches are per directory (ignored output). The watchers
// close with the repository.
func (r *Repository) Watch(skipDir func(rel string) bool) (<-chan Notice, error) {
	var ws []*watched
	add := func(root string, workTree bool) error {
		real, err := filepath.EvalSymlinks(root)
		if err != nil {
			return err
		}
		x := &watched{workTree: workTree}
		gd, _ := filepath.EvalSymlinks(r.GitDir)
		cd, _ := filepath.EvalSymlinks(r.CommonDir)
		if rel, ok := under(real, gd); ok {
			x.gitRel = rel
		}
		if rel, ok := under(real, cd); ok && cd != gd {
			x.commonRel = rel
		}
		skip := func(rel string) bool {
			// Objects churn and hold hundreds of directories; their changes
			// that matter (the commit-graph) are rare, and rescans catch them.
			for _, d := range []string{x.gitRel, x.commonRel} {
				if d != "" && (rel == d+"/objects" || (d == "." && rel == "objects") || rel == d+"/logs" || (d == "." && rel == "logs")) {
					return true
				}
			}
			return workTree && skipDir != nil && skipDir(rel)
		}
		w, err := watch.New(real, watch.Options{SkipDir: skip})
		if err != nil {
			return err
		}
		if _, err := r.life.Own(w); err != nil {
			return err
		}
		x.w = w
		ws = append(ws, x)
		return nil
	}
	covered := func(p string) bool {
		rp, _ := filepath.EvalSymlinks(p)
		for _, x := range ws {
			if _, ok := under(x.w.Root(), rp); ok {
				return true
			}
		}
		return false
	}
	if r.WorkTree != "" {
		if err := add(r.WorkTree, true); err != nil {
			return nil, err
		}
	}
	for _, dir := range []string{r.CommonDir, r.GitDir} {
		if !covered(dir) {
			if err := add(dir, false); err != nil {
				return nil, err
			}
		}
	}
	out := make(chan Notice, 1)
	merged := make(chan Notice)
	for _, x := range ws {
		x := x
		r.life.Go(func(ctx context.Context) {
			// The watcher closes after this returns, with the lifetime, so
			// the loop ends on the context, not on the channel.
			for {
				var b watch.Batch
				select {
				case b = <-x.w.Events():
				case <-ctx.Done():
					return
				}
				n := Notice{}
				if b.Overflow {
					n.What, n.Err = Rescan, x.w.Err()
				}
				for _, p := range b.Paths {
					c, wt := x.classify(p)
					n.What |= c
					if wt != "" {
						n.Paths = append(n.Paths, wt)
					}
				}
				if n.What == 0 {
					continue
				}
				select {
				case merged <- n:
				case <-ctx.Done():
					return
				}
			}
		})
	}
	r.life.Go(func(ctx context.Context) {
		defer close(out)
		for {
			select {
			case n := <-merged:
				// Coalesce with what is already waiting for the reader.
				select {
				case old := <-out:
					n.What |= old.What
					n.Paths = append(old.Paths, n.Paths...)
					if n.Err == nil {
						n.Err = old.Err
					}
				default:
				}
				if n.What&Rescan != 0 {
					n.Paths = nil
				}
				out <- n
			case <-ctx.Done():
				return
			}
		}
	})
	return out, nil
}
