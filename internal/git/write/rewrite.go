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
// (or the root), local changes stashed around it, and the branches
// pointing into the range moved where git can. action, when set, is what
// the todo does with rw.Commit in place of pick; the rebase stops before
// it starts unless the todo says so then. The op made of it sets
// Expect.Replay, which refuses a commit HEAD does not have and a range
// with a merge in it.
func (rw Rewrite) rebase(w Where, action string, extra ...string) run.Cmd {
	// The user's rebase.autoSquash would fold every fixup! commit of the
	// range into a commit the rewrite does not name; an --autosquash in
	// extra, later, wins.
	args := []string{"rebase", "--interactive", "--autostash", "--rebase-merges", "--no-autosquash"}
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
	// The todo is written as the edit below reads it, whatever the
	// user's config says: ids in full, commands in full, one line each.
	c.Config = append(c.Config, "core.abbrev=no", "rebase.abbreviateCommands=false", "rebase.instructionFormat=%s", "core.commentChar=#")
	editor := "true"
	if action != "" {
		// Both forms of pick are read, and the editor fails, which
		// stops the rebase before it starts, when no line was changed:
		// the rebase would replay every commit as it was and succeed.
		editor = "f() { sed -i.ramus -E -e 's/^(pick|p) " + rw.Commit + "( |$)/" + action + " " + rw.Commit + "\\2/' \"$1\" && " +
			"grep -q '^" + action + " " + rw.Commit + "' \"$1\"; }; f"
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
	fallback := !rw.Git.Has(gitexe.History)
	if fallback {
		c = rw.rebase(w, "reword")
	} else {
		c = w.cmd("history", "reword", "--update-refs=branches", "--end-of-options", rw.Commit)
	}
	c, in, err := withMessage(c, msg)
	if err != nil {
		return Op{}, err
	}
	op := rw.op("Reword", seen, c)
	op.Input = in
	if fallback {
		op.Expect.Replay = &rw
	}
	return op, nil
}

// DropCommit takes rw.Commit out of history, moving the commits and
// branches after it, through git history drop where git has it, else a
// rebase.
func DropCommit(w Where, rw Rewrite, seen Seen) (Op, error) {
	if err := rw.check(); err != nil {
		return Op{}, err
	}
	if !rw.Git.Has(gitexe.HistoryDrop) {
		op := rw.op("Drop", seen, rw.rebase(w, "drop"))
		op.Expect.Replay = &rw
		return op, nil
	}
	return rw.op("Drop", seen, w.cmd("history", "drop", "--update-refs=branches", "--end-of-options", rw.Commit)), nil
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
		op.Expect.Replay = &rw
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
	// message of the commit the fold makes; "" there joins the messages
	// whole, as git does but keeping lines that start with #.
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
		// A squash with no message joins the messages here, whole: git
		// joins them with comment lines of its own, and stripping those
		// strips the user's lines that start with its comment character.
		join := s.Action == DoSquash && msg == ""
		if msg != "" || join {
			// The new message is set once the commit is made, by an
			// amend git runs: git's own editor would ask for it.
			word = "pick"
			if s.Action == DoSquash {
				word = "fixup"
			}
			// HEAD before the step, for the amend to tell the step's
			// commit made from one skipped, which left HEAD on the commit
			// before: that one keeps its message.
			b.WriteString("exec git rev-parse -q --verify HEAD >\"$(git rev-parse --git-path " + stepHead + ")\" || :\n")
		}
		if join {
			b.WriteString("exec git log -1 --no-show-signature --format=%B HEAD >\"$(git rev-parse --git-path " + stepMessage + ")\"\n")
		}
		fmt.Fprintf(&b, "%s %s\n", word, s.Commit)
		if join {
			fmt.Fprintf(&b, "exec %s || "+
				"{ cat \"$(git rev-parse --git-path %s)\"; echo; git log -1 --no-show-signature --format=%%B %s; } | "+
				"git commit --amend --only -q --allow-empty --cleanup=whitespace -F -\n", skipped(s.Commit, word == "fixup"), stepMessage, s.Commit)
		}
		if msg != "" {
			cc, err := commentChar(msg)
			if err != nil {
				return "", err
			}
			fmt.Fprintf(&b, "exec %s || "+
				"printf '%%b' '%s' | git -c core.commentChar='%s' commit --amend --only -q --allow-empty -F -\n", skipped(s.Commit, word == "fixup"), printfArg(msg), cc)
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

// skipped is a shell test, run once the step on commit is over, that holds
// when the step was skipped: HEAD is where it was before it. A fold (fold
// set) can leave HEAD there too when it changes nothing, as within one
// second it makes the very commit HEAD is; such a fold cannot stop on a
// conflict, so it was not skipped.
func skipped(commit string, fold bool) string {
	same := `test "$(git rev-parse -q --verify HEAD)" = "$(cat "$(git rev-parse --git-path ` + stepHead + `)")"`
	if !fold {
		return same
	}
	// git merge-tree in its oldest form prints nothing when commit's
	// change, put on HEAD, leaves HEAD's tree as it is.
	noop := "git rev-parse -q --verify " + commit + "^ >/dev/null && test -z \"$(git merge-tree " + commit + "^ HEAD " + commit + ")\""
	return "{ " + same + " && ! { " + noop + "; }; }"
}

// stepHead is where a todo keeps HEAD as it was before a step: in the
// rebase's own folder, which goes with it.
const stepHead = "rebase-merge/ramus-step-head"

// stepMessage is where a todo keeps the message of the commit a squash
// folds into, to join the squashed commit's to.
const stepMessage = "rebase-merge/ramus-step-message"

// printfArg is s as printf %b reads it inside single quotes, on one line.
func printfArg(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`, "\r", `\r`, "'", `'\''`).Replace(s)
}
