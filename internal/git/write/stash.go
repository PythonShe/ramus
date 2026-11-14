package write

import (
	"errors"
	"strconv"
	"strings"

	"ramus/internal/git/run"
)

// StashOptions describe a stash of local changes.
type StashOptions struct {
	Message string
	// Untracked stashes untracked files too.
	Untracked bool
	// KeepIndex leaves what is staged in place as well as stashing it.
	KeepIndex bool
	// Paths limits the stash to these paths; none stashes everything.
	Paths []string
	// Staged stashes what is staged alone, leaving the rest in place: the
	// way to stash some hunks of a file (git 2.35, gitexe.StashStaged).
	// Untracked and KeepIndex do not apply.
	Staged bool
}

// Stash puts local changes on the stash and clears them from the work
// tree. Nothing is lost: the stash holds them.
func Stash(w Where, opt StashOptions) Op {
	args := []string{"stash", "push"}
	switch {
	case opt.Staged:
		args = append(args, "--staged")
	default:
		if opt.Untracked {
			args = append(args, "--include-untracked")
		}
		if opt.KeepIndex {
			args = append(args, "--keep-index")
		}
	}
	if m := strings.TrimSpace(opt.Message); m != "" {
		args = append(args, "--message="+m)
	}
	op := Op{Name: "Stash changes"}
	if opt.Staged {
		op.Name = "Stash staged changes"
	}
	if len(opt.Paths) > 0 {
		// git stash names the whole tree as ":/" itself, which literal
		// pathspecs break, so each path is made literal on its own.
		args = fromStdin(args...)
		lit := make([]string, len(opt.Paths))
		for i, p := range opt.Paths {
			lit[i] = ":(literal)" + p
		}
		op.Input = pathspecs(lit)
		op.Name = "Stash " + count(len(opt.Paths), opt.Paths[0])
	}
	op.Cmd = run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: args, Timeout: HookTimeout}
	return op
}

// StashEntry is a stash as the user saw it: its place in the list and its
// commit (hex). A write on it refuses if that place now holds another.
type StashEntry struct {
	N      int
	Commit string
}

func (s StashEntry) ref() string { return "stash@{" + strconv.Itoa(s.N) + "}" }

func (s StashEntry) expect() (*Expect, error) {
	if s.N < 0 || s.Commit == "" {
		return nil, errors.New("write: no stash chosen")
	}
	return &Expect{AnyHead: true, Refs: map[string]string{s.ref(): s.Commit}}, nil
}

// StashApply applies a stash to the work tree and keeps it; index also
// restores what was staged as staged. A conflict leaves markers in the
// files and the stash in place.
func StashApply(w Where, s StashEntry, index bool) (Op, error) {
	x, err := s.expect()
	if err != nil {
		return Op{}, err
	}
	args := []string{"stash", "apply"}
	if index {
		args = append(args, "--index")
	}
	return Op{Name: "Apply " + s.ref(), Expect: x, Cmd: w.cmd(append(args, s.Commit)...)}, nil
}

// StashPop applies a stash and drops it if that went cleanly.
func StashPop(w Where, s StashEntry, index bool) (Op, error) {
	x, err := s.expect()
	if err != nil {
		return Op{}, err
	}
	args := []string{"stash", "pop"}
	if index {
		args = append(args, "--index")
	}
	return Op{Name: "Pop " + s.ref() + " (" + short(s.Commit) + ")", Expect: x, Cmd: w.cmd(append(args, s.ref())...)}, nil
}

// StashDrop removes a stash from the list. Its commit stays in the
// object store until git prunes it, and StashRestore puts it back.
func StashDrop(w Where, s StashEntry) (Op, error) {
	x, err := s.expect()
	if err != nil {
		return Op{}, err
	}
	return Op{Name: "Drop " + s.ref() + " (" + short(s.Commit) + ")", Expect: x, Cmd: w.cmd("stash", "drop", "-q", s.ref())}, nil
}

// StashRestore puts a dropped stash's commit back on top of the list.
func StashRestore(w Where, commit, message string) (Op, error) {
	if !isHex(commit) {
		return Op{}, errors.New("write: a stash is restored by its commit")
	}
	if message == "" {
		message = "Restored stash " + short(commit)
	}
	return Op{Name: "Restore stash " + short(commit), Cmd: w.cmd("stash", "store", "--message="+message, commit)}, nil
}

// StashBranch makes a branch at the commit a stash was made on, checks it
// out, applies the stash there and drops it.
func StashBranch(w Where, s StashEntry, name string, seen Seen) (Op, error) {
	x, err := s.expect()
	if err != nil {
		return Op{}, err
	}
	if err := checkName("branch", name); err != nil {
		return Op{}, err
	}
	x.AnyHead = false
	x.Branch, x.Head = seen.Branch, seen.Head
	x.Refs["refs/heads/"+name] = ""
	return Op{Name: "Branch " + name + " from " + s.ref(), Expect: x, Undo: NotUndoable,
		Cmd: w.cmd("stash", "branch", name, s.ref())}, nil
}

func isHex(s string) bool {
	if len(s) < 4 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}
