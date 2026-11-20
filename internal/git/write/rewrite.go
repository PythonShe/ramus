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

// cleanMessage is msg as git's whitespace cleanup leaves it, which is how
// the commit box commits a message: trailing whitespace off every line,
// blank lines at the start and end dropped and runs of them made one, a
// final newline. Lines starting with a comment character stay.
func cleanMessage(msg string) string {
	var b strings.Builder
	blank := false
	for line := range strings.SplitSeq(msg, "\n") {
		// git's isspace: no vertical tab or form feed.
		line = strings.TrimRight(line, " \t\r")
		if line == "" {
			blank = b.Len() > 0
			continue
		}
		if blank {
			b.WriteByte('\n')
			blank = false
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func (rw Rewrite) check() error {
	if !isHex(rw.Commit) || (rw.Parent != "" && !isHex(rw.Parent)) {
		return fmt.Errorf("write: %q is not a commit id", rw.Commit)
	}
	return nil
}

// op is a rewrite of rw.Commit, backed up and undoable. rr decides, when
// its turn comes, whether git history or the rebase in rr.fallback runs,
// and checks the result.
func (rw Rewrite) op(name string, seen Seen, cmd run.Cmd, rr *rewriteRun) Op {
	cmd.Timeout = HookTimeout
	rr.fallback.Timeout = HookTimeout
	op := Op{Name: name + " " + short(rw.Commit), Undo: MovesHead, Expect: seen.expect(), Cmd: cmd, Refs: rw.Branches, rewrite: rr}
	if !rr.history {
		// The rebase refuses a commit HEAD does not have and a range with a
		// merge in it.
		op.Expect.Replay = &rr.rw
	}
	if seen.Branch != "" {
		op.Backup = []string{seen.Branch}
	}
	return op
}

// rebasePins are the settings every rebase a rewrite runs is given,
// whatever the user's config says. The todo is written as the edit reads
// it: ids in full, commands in full, one line each, # for comments, so a
// pick line is never read as a comment. The messages are stored as they
// are: commit.cleanup=strip would take the lines starting with # out of
// the commits replayed, and the messages the app sets are cleaned as the
// commit box cleans them before git sees them.
var rebasePins = []string{"core.abbrev=no", "rebase.abbreviateCommands=false", "rebase.instructionFormat=%s",
	"core.commentChar=#", "commit.cleanup=verbatim"}

// rebase is the fallback through git rebase: from the commit's parent
// (or the root), local changes stashed around it, and the branches
// pointing into the range moved where git can. action, when set, is what
// the todo does with rw.Commit in place of pick; the rebase stops before
// it starts unless the todo says so then. A commit that the rewrite
// leaves with no change stays, as git history is told too. The op made of
// it sets Expect.Replay, which refuses a commit HEAD does not have and a
// range with a merge in it.
func (rw Rewrite) rebase(w Where, action string, extra ...string) run.Cmd {
	// The user's rebase.autoSquash would fold every fixup! commit of the
	// range into a commit the rewrite does not name.
	args := []string{"rebase", "--interactive", "--autostash", "--rebase-merges", "--no-autosquash", "--empty=keep"}
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
	c.Config = append(c.Config, rebasePins...)
	editor := "true"
	if action != "" {
		// Both forms of pick are read, and the editor fails, which
		// stops the rebase before it starts, when no line was changed:
		// the rebase would replay every commit as it was and succeed.
		editor = "f() { sed -i.ramus -E -e 's/^(pick|p) " + rw.Commit + "( |$)/" + action + " " + rw.Commit + "\\2/' \"$1\" && " +
			"grep -q '^" + action + " " + rw.Commit + "' \"$1\"; }; f"
	}
	// No message is asked for unless withMessage answers it: the user's
	// editor would open where the app shows nothing.
	c.Env = append(c.Env, "GIT_SEQUENCE_EDITOR="+editor, "GIT_EDITOR=true")
	return c
}

// fixupEnv names the todo line a fix up's rebase puts after the pick of
// its commit, set when the commit holding what is staged is made.
const fixupEnv = "RAMUS_FIXUP_LINE"

// fixupRebase is the rebase that folds the commit fixupEnv names into
// rw.Commit, and leaves every other commit of the range as it is: the
// line is placed by commit id, never by autosquash, which would fold the
// user's own fixup! commits too and match the target by its subject.
func (rw Rewrite) fixupRebase(w Where) run.Cmd {
	c := rw.rebase(w, "")
	// It fails, which stops the rebase before it starts, unless the line
	// went in once. The commit the line names is HEAD when the rebase
	// starts, so the todo also picks it, last: that pick goes.
	editor := "f() { test -n \"$" + fixupEnv + "\" && " +
		"awk -v l=\"$" + fixupEnv + "\" 'BEGIN { k = split(l, w, \" \"); x = w[k] } ($1 == \"pick\" || $1 == \"p\") && $2 == x { next } " +
		"{ print } ($1 == \"pick\" || $1 == \"p\") && $2 == \"" + rw.Commit + "\" { print l; n++ } END { exit n != 1 }' \"$1\" >\"$1.ramus\" && " +
		"mv -f \"$1.ramus\" \"$1\"; }; f"
	c.Env = setEnv(c.Env, "GIT_SEQUENCE_EDITOR", editor)
	return c
}

// setEnv is env with key set to value, once.
func setEnv(env []string, key, value string) []string {
	out := env[:0:0]
	for _, e := range env {
		if !strings.HasPrefix(e, key+"=") {
			out = append(out, e)
		}
	}
	return append(out, key+"="+value)
}

// withMessage has git's editor write msg, fed on standard input, for
// the one message the command asks for.
func withMessage(c run.Cmd, msg string) (run.Cmd, []byte, error) {
	cc, err := commentChar(msg)
	if err != nil {
		return c, nil, err
	}
	c.Config = append(c.Config, "core.commentChar="+cc)
	c.Env = setEnv(c.Env, "GIT_EDITOR", "cat >")
	return c, []byte(msg), nil
}

// historyEmpty reports whether git history takes --empty, which keeps a
// commit the rewrite leaves with no change instead of dropping it.
func historyEmpty(v gitexe.Version) bool { return v.Has(gitexe.HistoryDrop) }

// Reword gives rw.Commit message msg, moving the commits and branches
// after it, through git history reword where git has it, else a rebase.
// The message is cleaned as the commit box cleans one.
func Reword(w Where, rw Rewrite, msg string, seen Seen) (Op, error) {
	if err := rw.check(); err != nil {
		return Op{}, err
	}
	msg = cleanMessage(msg)
	if msg == "" {
		return Op{}, errors.New("write: a commit message cannot be empty")
	}
	rr := &rewriteRun{kind: rewordKind, rw: rw, msg: msg, history: rw.Git.Has(gitexe.History)}
	var err error
	if rr.fallback, _, err = withMessage(rw.rebase(w, "reword"), msg); err != nil {
		return Op{}, err
	}
	c := rr.fallback
	if rr.history {
		if c, _, err = withMessage(w.cmd("history", "reword", "--update-refs=branches", "--end-of-options", rw.Commit), msg); err != nil {
			return Op{}, err
		}
	}
	op := rw.op("Reword", seen, c, rr)
	op.Input = []byte(msg)
	return op, nil
}

// DropCommit takes rw.Commit out of history, moving the commits and
// branches after it, through git history drop where git has it, else a
// rebase.
func DropCommit(w Where, rw Rewrite, seen Seen) (Op, error) {
	if err := rw.check(); err != nil {
		return Op{}, err
	}
	rr := &rewriteRun{kind: dropKind, rw: rw, history: rw.Git.Has(gitexe.HistoryDrop) && historyEmpty(rw.Git), fallback: rw.rebase(w, "drop")}
	c := rr.fallback
	if rr.history {
		c = w.cmd("history", "drop", "--update-refs=branches", "--empty=keep", "--end-of-options", rw.Commit)
		c.Env = append(c.Env, "GIT_EDITOR=true")
	}
	return rw.op("Drop", seen, c, rr), nil
}

// FixupInto folds what is staged into rw.Commit, and only into it. With
// msg set, the commit takes it as its message (a squash). Through git
// history fixup where git has it, else a rebase that folds a commit of
// what is staged, made without hooks, into rw.Commit by its id. Undo
// moves the branches back and leaves the index and files: what was
// folded in is staged again, as it was.
func FixupInto(w Where, rw Rewrite, msg string, seen Seen) (Op, error) {
	if err := rw.check(); err != nil {
		return Op{}, err
	}
	name := "Fix up"
	if msg != "" {
		name = "Squash into"
		if msg = cleanMessage(msg); msg == "" {
			return Op{}, errors.New("write: a commit message cannot be empty")
		}
	}
	rr := &rewriteRun{kind: fixupKind, rw: rw, msg: msg, history: rw.Git.Has(gitexe.HistoryFixup) && historyEmpty(rw.Git), fallback: rw.fixupRebase(w)}
	c := rr.fallback
	var in []byte
	if rr.history {
		args := []string{"history", "fixup", "--update-refs=branches", "--empty=keep"}
		if msg != "" {
			args = append(args, "--reedit-message")
		}
		c = w.cmd(append(args, "--end-of-options", rw.Commit)...)
		c.Env = append(c.Env, "GIT_EDITOR=true")
		if msg != "" {
			var err error
			if c, in, err = withMessage(c, msg); err != nil {
				return Op{}, err
			}
		}
	}
	op := rw.op(name, seen, c, rr)
	// HEAD and the branches move, the index and the files stay: undo moves
	// them back, and what was staged is staged again.
	op.Undo = RefsOnly
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
// must be a straight line, with no merge among them, and the steps must
// name each of them once. A step's Refs move only where git has
// update-ref; without, branches stay where they are.
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
	args := []string{"rebase", "--interactive", "--autostash", "--no-rebase-merges", "--no-autosquash", "--empty=keep"}
	if rw.Parent == "" {
		args = append(args, "--root")
	} else {
		args = append(args, "--end-of-options", rw.Parent)
	}
	c := w.cmd(args...)
	c.Config = append(c.Config, rebasePins...)
	// The todo comes on standard input; the messages of rewords and
	// squashes come from the todo, never from the user's editor.
	c.Env = append(c.Env, "GIT_SEQUENCE_EDITOR=cat >", "GIT_EDITOR="+stepEditor)
	rr := &rewriteRun{kind: editKind, rw: rw, steps: steps, fallback: c}
	op := rw.op("Edit history from", seen, c, rr)
	op.Expect.Replay = nil // the steps are checked against the range instead
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
			if msg = cleanMessage(s.Message); msg == "" {
				return "", errors.New("write: a commit message cannot be empty")
			}
		case DoSquash, DoFixup:
			if !kept {
				return "", fmt.Errorf("write: %s has no commit before it to fold into", short(s.Commit))
			}
			if s.Action == DoSquash && s.Message != "" {
				if msg = cleanMessage(s.Message); msg == "" {
					return "", errors.New("write: a commit message cannot be empty")
				}
			}
		default:
			return "", fmt.Errorf("write: unknown %v", s.Action)
		}
		// A squash with no message joins the messages here, whole: git
		// joins them with comment lines of its own, and stripping those
		// strips the user's lines that start with its comment character.
		join := s.Action == DoSquash && msg == ""
		if msg != "" || join {
			// git's own reword and squash make the commit, so its hooks run
			// as they do in a terminal; the message is put where the editor
			// git opens for it (stepEditor) takes it from.
			// HEAD before the step, for the step's end to tell the step's
			// commit made from one skipped, which left HEAD on the commit
			// before: that one keeps its message.
			b.WriteString("exec git rev-parse -q --verify HEAD >\"$(git rev-parse --git-path " + stepHead + ")\" || :\n")
			// Cleaned as the commit box cleans a message, whatever
			// commit.cleanup says: git stripspace is --cleanup=whitespace.
			if join {
				fmt.Fprintf(&b, "exec { git log -1 --no-show-signature --format=%%B HEAD; echo; git log -1 --no-show-signature --format=%%B %s; } | "+
					"git stripspace >\"$(git rev-parse --git-path %s)\"\n", s.Commit, stepMessage)
			} else {
				fmt.Fprintf(&b, "exec printf '%%b' '%s' >\"$(git rev-parse --git-path %s)\"\n", printfArg(msg), stepMessage)
			}
		}
		fmt.Fprintf(&b, "%s %s\n", s.Action, s.Commit)
		if msg != "" || join {
			// The editor did not take the message when the step stopped on
			// a conflict and was then committed by a continue, which gives
			// git's: it is set by an amend. A step skipped leaves it unused.
			fmt.Fprintf(&b, "exec m=\"$(git rev-parse --git-path %s)\" && "+
				"if test -f \"$m\"; then { %s || git commit --amend --only -q --allow-empty --cleanup=whitespace -F \"$m\"; } && rm -f \"$m\"; fi\n",
				stepMessage, skipped(s.Commit, s.Action == DoSquash))
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

// stepMessage is where a todo keeps the message of the step to come, a
// reword's or a squash's, for stepEditor to give git.
const stepMessage = "rebase-merge/ramus-step-message"

// stepEditor is the editor of an edit of history: it gives git the
// message stepMessage holds, once, and otherwise leaves git's as it is.
const stepEditor = "f() { m=\"$(git rev-parse --git-path " + stepMessage + ")\" && " +
	"if test -f \"$m\"; then cat \"$m\" >\"$1\" && rm -f \"$m\"; fi; }; f"

// printfArg is s as printf %b reads it inside single quotes, on one line.
func printfArg(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`, "\r", `\r`, "'", `'\''`).Replace(s)
}
