package write

import (
	"errors"
	"path/filepath"
	"strings"

	"ramus/internal/git/run"
)

// WorktreeOptions say what a new work tree checks out.
type WorktreeOptions struct {
	// Path is where it goes, absolute; git makes the folder, which must be
	// missing or empty.
	Path string
	// Branch is a local branch to check out there, by short name. With
	// NewBranch set instead, a branch of that name is made from Start
	// (a commit, a branch or a remote branch; HEAD when empty), tracking
	// Start when Track is set, as for a remote branch.
	Branch    string
	NewBranch string
	Start     string
	Track     bool
}

// AddWorktree makes a work tree of the repository.
func AddWorktree(w Where, o WorktreeOptions) (Op, error) {
	if !filepath.IsAbs(o.Path) {
		return Op{}, errors.New("write: a worktree's folder must be an absolute path")
	}
	args := []string{"worktree", "add"}
	op := Op{}
	switch {
	case o.NewBranch != "":
		name := strings.TrimPrefix(o.NewBranch, "refs/heads/")
		if err := checkName("branch", name); err != nil {
			return Op{}, err
		}
		if o.Track {
			args = append(args, "--track")
		} else {
			args = append(args, "--no-track")
		}
		args = append(args, "-b", name, "--", o.Path)
		if o.Start != "" {
			args = append(args, o.Start)
		}
		op.Name = "Add worktree with new branch " + name
		op.Refs = []string{"refs/heads/" + name}
	case o.Branch != "":
		name := strings.TrimPrefix(o.Branch, "refs/heads/")
		if err := checkName("branch", name); err != nil {
			return Op{}, err
		}
		// The short name: git checks out a full ref name detached.
		args = append(args, "--", o.Path, name)
		op.Name = "Add worktree of " + name
	default:
		return Op{}, errors.New("write: a worktree needs a branch")
	}
	op.Cmd = run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: args, Timeout: HookTimeout}
	return op, nil
}

// RemoveWorktree removes the work tree at path and its folder; git refuses
// when it has changes or untracked files, unless force is set, and always
// when it is locked.
func RemoveWorktree(w Where, path string, force bool) Op {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	return Op{Name: "Remove worktree " + filepath.Base(path),
		Cmd: run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: append(args, "--", path)}}
}

// PruneWorktrees forgets the work trees whose folders are gone.
func PruneWorktrees(w Where) Op {
	return Op{Name: "Prune worktrees", Cmd: run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: []string{"worktree", "prune", "--expire=now"}}}
}

// LockWorktree keeps the work tree at path from being pruned, moved or
// removed, for reason when it is not empty: one on a drive not always
// there.
func LockWorktree(w Where, path, reason string) Op {
	args := []string{"worktree", "lock"}
	if r := strings.TrimSpace(reason); r != "" {
		args = append(args, "--reason="+r)
	}
	return Op{Name: "Lock worktree " + filepath.Base(path),
		Cmd: run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: append(args, "--", path)}}
}

// UnlockWorktree undoes LockWorktree.
func UnlockWorktree(w Where, path string) Op {
	return Op{Name: "Unlock worktree " + filepath.Base(path),
		Cmd: run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: []string{"worktree", "unlock", "--", path}}}
}
