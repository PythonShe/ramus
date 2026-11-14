package write

import (
	"errors"
	"strings"

	"ramus/internal/git/run"
	"ramus/internal/git/snapshot"
)

// Seen is HEAD as the user saw it when choosing an action: the branch it
// named ("refs/heads/main", "" when detached) and its commit (hex, ""
// when unborn). A write built on it refuses if HEAD moved since.
type Seen struct {
	Branch, Head string
}

func (s Seen) expect() *Expect { return &Expect{Branch: s.Branch, Head: s.Head} }

// Switch checks out a local branch. git refuses when local changes would
// be overwritten, so nothing is dropped; undo switches back.
func Switch(w Where, branch string, seen Seen) Op {
	name := strings.TrimPrefix(branch, "refs/heads/")
	return Op{Name: "Switch to " + name, Undo: MovesHead, Expect: seen.expect(),
		Cmd: run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: []string{"switch", "--no-guess", "--end-of-options", name}, Timeout: HookTimeout}}
}

// Detach checks out commit (hex) with no branch, as for looking at an old
// state.
func Detach(w Where, commit string, seen Seen) Op {
	return Op{Name: "Check out " + short(commit), Undo: MovesHead, Expect: seen.expect(),
		Cmd: run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: []string{"switch", "--detach", "--end-of-options", commit}, Timeout: HookTimeout}}
}

// CreateBranch makes branch name at commit (hex), and switches to it when
// checkout is set. It refuses if the branch exists; git checks the name.
func CreateBranch(w Where, name, commit string, checkout bool, seen Seen) (Op, error) {
	name = strings.TrimPrefix(name, "refs/heads/")
	if name == "" || strings.HasPrefix(name, "-") {
		return Op{}, errors.New("write: a branch name cannot be empty or start with -")
	}
	ref := "refs/heads/" + name
	x := seen.expect()
	x.Refs = map[string]string{ref: ""}
	op := Op{Name: "Create branch " + name, Undo: RefsOnly, Refs: []string{ref}, Expect: x}
	args := []string{"branch", "--no-track", "--end-of-options", name, commit}
	if checkout {
		op.Undo = MovesHead
		args = []string{"switch", "--no-track", "-c", name, "--end-of-options", commit}
	}
	op.Cmd = run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: args, Timeout: HookTimeout}
	return op, nil
}

// ResetMode is what a reset does to the index and the work tree.
type ResetMode string

const (
	// ResetSoft moves the branch and keeps the index and files.
	ResetSoft ResetMode = "soft"
	// ResetMixed also resets the index, keeping the files.
	ResetMixed ResetMode = "mixed"
	// ResetKeep also updates the files, refusing to drop local changes.
	ResetKeep ResetMode = "keep"
	// ResetHard makes the index and files match the commit, dropping
	// local changes; they are kept in a snapshot first.
	ResetHard ResetMode = "hard"
)

// Reset moves the branch HEAD is on (or HEAD, detached) to commit (hex).
// The branch's value is backed up first, so commits it leaves stay
// reachable.
func Reset(w Where, commit string, mode ResetMode, seen Seen) (Op, error) {
	switch mode {
	case ResetSoft, ResetMixed, ResetKeep, ResetHard:
	default:
		return Op{}, errors.New("write: unknown reset mode " + string(mode))
	}
	if seen.Head == "" {
		return Op{}, errors.New("write: there is no commit to reset from")
	}
	op := Op{Name: "Reset to " + short(commit), Undo: MovesHead, Expect: seen.expect(),
		Cmd: run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: []string{"reset", "-q", "--" + string(mode), commit, "--"}}}
	if seen.Branch != "" {
		op.Backup = []string{seen.Branch}
	}
	if mode == ResetHard {
		op.Name = "Hard reset to " + short(commit)
		op.Snapshot = &snapshot.Options{Reason: op.Name}
	}
	return op, nil
}

// LocalChanges is what a switch does with uncommitted changes.
type LocalChanges uint8

const (
	// CarryChanges takes them along, as git switch does; git refuses
	// when they would be overwritten.
	CarryChanges LocalChanges = iota
	// StashChanges stashes them first, untracked files included.
	StashChanges
)

// SwitchWith checks out a local branch, doing with local changes what
// changes says.
func SwitchWith(w Where, branch string, changes LocalChanges, seen Seen) Op {
	op := Switch(w, branch, seen)
	if changes == StashChanges {
		name := strings.TrimPrefix(branch, "refs/heads/")
		op.Name += ", stashing changes"
		op.Pre = &run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Timeout: HookTimeout,
			Args: []string{"stash", "push", "--include-untracked", "-m", "Ramus: before switching to " + name}}
	}
	return op
}

// checkName refuses a name git would read as an option.
func checkName(what, name string) error {
	if name == "" || strings.HasPrefix(name, "-") {
		return errors.New("write: a " + what + " name cannot be empty or start with -")
	}
	return nil
}

// RenameBranch renames a local branch; git checks the new name, and
// moves its config and reflog with it.
func RenameBranch(w Where, from, to string, seen Seen) (Op, error) {
	from, to = strings.TrimPrefix(from, "refs/heads/"), strings.TrimPrefix(to, "refs/heads/")
	if err := checkName("branch", to); err != nil {
		return Op{}, err
	}
	x := seen.expect()
	x.Refs = map[string]string{"refs/heads/" + to: ""}
	return Op{Name: "Rename branch " + from + " to " + to, Undo: RefsOnly, Expect: x,
		Refs: []string{"refs/heads/" + from, "refs/heads/" + to},
		Cmd:  run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: []string{"branch", "-m", "--end-of-options", from, to}}}, nil
}

// DeleteBranch deletes a local branch at commit (hex), which the user saw
// it at; force deletes it though not merged. Its value is backed up
// first, and undo puts it back. A worktree that has it checked out is
// removed first, which git refuses when the worktree has changes.
func DeleteBranch(w Where, branch, commit string, force bool, worktree string, seen Seen) Op {
	name := strings.TrimPrefix(branch, "refs/heads/")
	ref := "refs/heads/" + name
	x := seen.expect()
	x.Refs = map[string]string{ref: commit}
	flag := "-d"
	if force {
		flag = "-D"
	}
	op := Op{Name: "Delete branch " + name + " at " + short(commit), Undo: RefsOnly, Expect: x, Refs: []string{ref},
		Backup: []string{ref},
		Cmd:    run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: []string{"branch", flag, "--end-of-options", name}}}
	if worktree != "" {
		op.Pre = &run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: []string{"worktree", "remove", "--", worktree}}
	}
	return op
}

// MergeMode is how a merge records itself.
type MergeMode string

const (
	// MergeDefault fast-forwards when it can, else makes a merge commit.
	MergeDefault MergeMode = ""
	// MergeFFOnly only fast-forwards, refusing otherwise.
	MergeFFOnly MergeMode = "--ff-only"
	// MergeNoFF always makes a merge commit.
	MergeNoFF MergeMode = "--no-ff"
	// MergeSquash stages the merged changes for one new commit, and
	// commits nothing.
	MergeSquash MergeMode = "--squash"
)

// Merge merges commit-ish what (a branch or hex) into the branch HEAD is
// on, which is backed up first. A conflict leaves the merge in progress.
func Merge(w Where, what string, mode MergeMode, seen Seen) (Op, error) {
	if err := checkName("merge", what); err != nil {
		return Op{}, err
	}
	switch mode {
	case MergeDefault, MergeFFOnly, MergeNoFF, MergeSquash:
	default:
		return Op{}, errors.New("write: unknown merge mode " + string(mode))
	}
	args := []string{"merge", "--no-edit"}
	if mode != MergeDefault {
		args = append(args, string(mode))
	}
	op := Op{Name: "Merge " + strings.TrimPrefix(what, "refs/heads/"), Undo: MovesHead, Expect: seen.expect(),
		Cmd: run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: append(args, "--end-of-options", what), Timeout: HookTimeout}}
	if seen.Branch != "" {
		op.Backup = []string{seen.Branch}
	}
	return op, nil
}

// Rebase replays the branch HEAD is on onto commit-ish onto, backing the
// branch up first, and moves the branches that point into what it
// replays with it (rebase.updateRefs; git before 2.38 leaves them).
// branches are the local branches it may move, which undo moves back. A
// conflict leaves the rebase in progress.
func Rebase(w Where, onto string, branches []string, seen Seen) (Op, error) {
	if err := checkName("rebase", onto); err != nil {
		return Op{}, err
	}
	if seen.Branch == "" {
		return Op{}, errors.New("write: rebase needs a branch checked out")
	}
	return Op{Name: "Rebase onto " + strings.TrimPrefix(onto, "refs/heads/"), Undo: MovesHead, Expect: seen.expect(),
		Backup: []string{seen.Branch}, Refs: branches,
		Cmd: run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: []string{"rebase", "--end-of-options", onto},
			Config: []string{updateRefs}, Timeout: HookTimeout}}, nil
}

// updateRefs has a rebase move the branches in the range it replays.
const updateRefs = "rebase.updateRefs=true"
