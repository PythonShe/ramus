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
