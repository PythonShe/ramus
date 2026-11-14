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

// Action is what an edit of history does with one commit.
type Action uint8

const (
	DoPick Action = iota
	DoReword
	// DoEdit stops the rebase after the commit, for the user to change it.
	DoEdit
	// DoSquash folds the commit into the one before it, messages joined.
	DoSquash
	// DoFixup folds the commit into the one before it, keeping that
	// one's message.
	DoFixup
	DoDrop
)

var actionWords = [...]string{"pick", "reword", "edit", "squash", "fixup", "drop"}

func (a Action) String() string {
	if int(a) < len(actionWords) {
		return actionWords[a]
	}
	return "action " + fmt.Sprint(uint8(a))
}

// Step is one commit of an edit of history.
type Step struct {
	Action Action
	Commit string // hex
	// Message is the commit's new message for DoReword, and for DoSquash the
	// message of the commit the fold makes; "" there joins the messages,
	// as git does.
	Message string
	// Refs are branches (full names) to leave at this step's commit, as
	// it is once the step is done.
	Refs []string
}

// EditHistory replays steps, oldest first, onto rw.Parent (the root when
// it is ""), as an interactive rebase does with the todo they make; rw.Commit
// is the oldest commit replayed. The todo is written whole, so the commits
// must be a straight line, with no merge among them. A step's Refs move
// only where git has update-ref; without, branches stay where they are.
func EditHistory(w Where, rw Rewrite, steps []Step, seen Seen) (Op, error) {
	if err := rw.check(); err != nil {
		return Op{}, err
	}
	if len(steps) == 0 {
		return Op{}, errors.New("write: no commits to replay")
	}
	todo, err := historyTodo(steps, rw.Git.Has(gitexe.UpdateRefs))
	if err != nil {
		return Op{}, err
	}
	args := []string{"rebase", "--interactive", "--autostash", "--no-rebase-merges"}
	if rw.Parent == "" {
		args = append(args, "--root")
	} else {
		args = append(args, "--end-of-options", rw.Parent)
	}
	c := w.cmd(args...)
	// The todo comes on standard input; a squash keeps the messages
	// joined, which git's editor would otherwise be asked about.
	c.Env = append(c.Env, "GIT_SEQUENCE_EDITOR=cat >", "GIT_EDITOR=true")
	op := rw.op("Edit history from", seen, c)
	op.Input = []byte(todo)
	return op, nil
}

// historyTodo is the rebase todo steps make; refs adds the update-ref
// lines.
func historyTodo(steps []Step, refs bool) (string, error) {
	var b strings.Builder
	seen := map[string]bool{}
	kept := false
	for _, s := range steps {
		if !isHex(s.Commit) {
			return "", fmt.Errorf("write: %q is not a commit id", s.Commit)
		}
		if seen[s.Commit] {
			return "", fmt.Errorf("write: %s is in the list twice", short(s.Commit))
		}
		seen[s.Commit] = true
		msg := ""
		switch s.Action {
		case DoPick, DoEdit, DoDrop:
		case DoReword:
			if strings.TrimSpace(s.Message) == "" {
				return "", errors.New("write: a commit message cannot be empty")
			}
			msg = s.Message
		case DoSquash, DoFixup:
			if !kept {
				return "", fmt.Errorf("write: %s has no commit before it to fold into", short(s.Commit))
			}
			if s.Action == DoSquash && s.Message != "" {
				if strings.TrimSpace(s.Message) == "" {
					return "", errors.New("write: a commit message cannot be empty")
				}
				msg = s.Message
			}
		default:
			return "", fmt.Errorf("write: unknown %v", s.Action)
		}
		word := s.Action.String()
		if msg != "" {
			// The new message is set once the commit is made, by an
			// amend git runs: git's own editor would ask for it.
			word = "pick"
			if s.Action == DoSquash {
				word = "fixup"
			}
		}
		fmt.Fprintf(&b, "%s %s\n", word, s.Commit)
		if msg != "" {
			cc, err := commentChar(msg)
			if err != nil {
				return "", err
			}
			fmt.Fprintf(&b, "exec printf '%%b' '%s' | git -c core.commentChar='%s' commit --amend --only -q --allow-empty -F -\n", printfArg(msg), cc)
		}
		kept = kept || s.Action != DoDrop
		if !refs {
			continue
		}
		for _, r := range s.Refs {
			if !strings.HasPrefix(r, "refs/heads/") || strings.ContainsAny(r, " \t\n") {
				return "", fmt.Errorf("write: %q is not a branch", r)
			}
			fmt.Fprintf(&b, "update-ref %s\n", r)
		}
	}
	return b.String(), nil
}

// printfArg is s as printf %b reads it inside single quotes, on one line.
func printfArg(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`, "\r", `\r`, "'", `'\''`).Replace(s)
}
