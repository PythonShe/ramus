package write

import (
	"errors"
	"fmt"
	"strings"

	"ramus/internal/git/gitexe"
	"ramus/internal/git/run"
)

// Rewrite names a commit of HEAD's history to rewrite, and what the
// rewrite needs to know: the git at hand, the commit's first parent (hex,
// "" for a root commit), and the local branches it may move, which undo
// moves back.
type Rewrite struct {
	Commit, Parent string
	Git            gitexe.Version
	Branches       []string
}

// ErrNeedsNewerGit is a rewrite this git cannot do in one step.
var ErrNeedsNewerGit = errors.New("write: this needs a newer git")

// commentChars are tried, in order, as the comment character of a
// message written through an editor: one that no line starts with keeps
// every line, as git history strips comments whatever commit.cleanup
// says.
const commentChars = "#;@!$%^&|:"

func commentChar(msg string) (string, error) {
	for _, c := range commentChars {
		starts := false
		for _, line := range strings.Split(msg, "\n") {
			if strings.HasPrefix(line, string(c)) {
				starts = true
				break
			}
		}
		if !starts {
			return string(c), nil
		}
	}
	return "", errors.New("write: every comment character starts a line of the message")
}

func (rw Rewrite) check() error {
	if !isHex(rw.Commit) || (rw.Parent != "" && !isHex(rw.Parent)) {
		return fmt.Errorf("write: %q is not a commit id", rw.Commit)
	}
	return nil
}

// op is a rewrite of rw.Commit, backed up and undoable.
func (rw Rewrite) op(name string, seen Seen, cmd run.Cmd) Op {
	cmd.Timeout = HookTimeout
	op := Op{Name: name + " " + short(rw.Commit), Undo: MovesHead, Expect: seen.expect(), Cmd: cmd, Refs: rw.Branches}
	if seen.Branch != "" {
		op.Backup = []string{seen.Branch}
	}
	return op
}

// rebase is the fallback through git rebase: from the commit's parent
// (or the root), merges kept, local changes stashed around it, and the
// branches pointing into the range moved where git can. editTodo is a
// sed expression for the todo, whose ids are written in full.
func (rw Rewrite) rebase(w Where, editTodo string, extra ...string) run.Cmd {
	args := []string{"rebase", "--interactive", "--autostash", "--rebase-merges"}
	if rw.Git.Has(gitexe.UpdateRefs) {
		args = append(args, "--update-refs")
	}
	args = append(args, extra...)
	if rw.Parent == "" {
		args = append(args, "--root")
	} else {
		args = append(args, "--end-of-options", rw.Parent)
	}
	c := w.cmd(args...)
	c.Config = append(c.Config, "core.abbrev=no")
	editor := "true"
	if editTodo != "" {
		editor = "sed -i.ramus -e '" + editTodo + "'"
	}
	c.Env = append(c.Env, "GIT_SEQUENCE_EDITOR="+editor)
	return c
}

// withMessage has git's editor write msg, fed on standard input, for
// the one message the command asks for.
func withMessage(c run.Cmd, msg string) (run.Cmd, []byte, error) {
	cc, err := commentChar(msg)
	if err != nil {
		return c, nil, err
	}
	c.Config = append(c.Config, "core.commentChar="+cc)
	c.Env = append(c.Env, "GIT_EDITOR=cat >")
	return c, []byte(msg), nil
}

// Reword gives rw.Commit message msg, moving the commits and branches
// after it, through git history reword where git has it, else a rebase.
func Reword(w Where, rw Rewrite, msg string, seen Seen) (Op, error) {
	if err := rw.check(); err != nil {
		return Op{}, err
	}
	if strings.TrimSpace(msg) == "" {
		return Op{}, errors.New("write: a commit message cannot be empty")
	}
	var c run.Cmd
	if rw.Git.Has(gitexe.History) {
		c = w.cmd("history", "reword", "--update-refs=branches", "--end-of-options", rw.Commit)
	} else {
		c = rw.rebase(w, "s/^pick "+rw.Commit+" /reword "+rw.Commit+" /")
	}
	c, in, err := withMessage(c, msg)
	if err != nil {
		return Op{}, err
	}
	op := rw.op("Reword", seen, c)
	op.Input = in
	return op, nil
}

// DropCommit takes rw.Commit out of history, moving the commits and
// branches after it, through git history drop where git has it, else a
// rebase.
func DropCommit(w Where, rw Rewrite, seen Seen) (Op, error) {
	if err := rw.check(); err != nil {
		return Op{}, err
	}
	var c run.Cmd
	if rw.Git.Has(gitexe.HistoryDrop) {
		c = w.cmd("history", "drop", "--update-refs=branches", "--end-of-options", rw.Commit)
	} else {
		c = rw.rebase(w, "s/^pick "+rw.Commit+" /drop "+rw.Commit+" /")
	}
	return rw.op("Drop", seen, c), nil
}

// FixupInto folds what is staged into rw.Commit. With msg set, the
// commit takes it as its message (a squash); that needs git history
// fixup. Without, the message stays, through git history fixup where git
// has it, else a fixup commit and an autosquash rebase.
func FixupInto(w Where, rw Rewrite, msg string, seen Seen) (Op, error) {
	if err := rw.check(); err != nil {
		return Op{}, err
	}
	if !rw.Git.Has(gitexe.HistoryFixup) {
		if msg != "" {
			return Op{}, ErrNeedsNewerGit
		}
		op := rw.op("Fix up", seen, rw.rebase(w, "", "--autosquash"))
		pre := w.cmd("commit", "--fixup="+rw.Commit)
		pre.Timeout = HookTimeout
		op.Pre = &pre
		return op, nil
	}
	args := []string{"history", "fixup", "--update-refs=branches"}
	if msg != "" {
		args = append(args, "--reedit-message")
	}
	c := w.cmd(append(args, "--end-of-options", rw.Commit)...)
	name := "Fix up"
	var in []byte
	if msg != "" {
		name = "Squash into"
		var err error
		if c, in, err = withMessage(c, msg); err != nil {
			return Op{}, err
		}
	}
	op := rw.op(name, seen, c)
	op.Input = in
	return op, nil
}
