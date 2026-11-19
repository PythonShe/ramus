package write

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"ramus/internal/git/run"
)

// popStash puts back the changes the write of e stashed before it ran,
// once its undo took HEAD back: the stash is applied by its commit, then
// dropped where the list holds it then, never as a position found
// before, which a stash pushed meanwhile moves. When that fails, the
// error names the stash they stay in.
func (q *Queue) popStash(ctx context.Context, e Entry) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), unstashTimeout)
	defer cancel()
	workTree := e.op.Cmd.WorkTree
	git := func(args ...string) (run.Result, error) {
		return q.r.Run(ctx, &run.Cmd{GitDir: q.gitDir, WorkTree: workTree, Timeout: unstashTimeout, Untranslated: true, Args: args})
	}
	entry, err := q.stashEntry(ctx, workTree, e.stash)
	if err == nil {
		_, err = git("stash", "apply", "--index", e.stash)
	}
	if err != nil {
		return &StashKeptError{Err: fmt.Errorf("write: undo of %s went back", e.Name), Entry: entry, Commit: e.stash, Pop: err}
	}
	// The changes are back; what is left is to drop the stash.
	if entry, err = q.stashEntry(ctx, workTree, e.stash); err == nil {
		if q.beforeDrop != nil {
			q.beforeDrop()
		}
		var res run.Result
		if res, err = git("stash", "drop", entry); err == nil {
			err = q.putBack(ctx, git, e.stash, string(res.Stdout))
		}
	}
	if err != nil {
		return &StashKeptError{Err: fmt.Errorf("write: undo of %s went back and its stashed changes are back, but the stash was not dropped", e.Name),
			Entry: entry, Commit: e.stash, Pop: err}
	}
	return nil
}

// stashEntry is the stash list's entry for the stash commit id.
func (q *Queue) stashEntry(ctx context.Context, workTree, id string) (string, error) {
	res, err := q.r.Run(ctx, &run.Cmd{GitDir: q.gitDir, WorkTree: workTree, Read: true,
		Args: []string{"log", "-g", "--format=%H", "refs/stash", "--"}})
	if err != nil {
		return "", err
	}
	for n, line := range strings.Split(strings.TrimSpace(string(res.Stdout)), "\n") {
		if line == id {
			return "stash@{" + strconv.Itoa(n) + "}", nil
		}
	}
	return "", errors.New("write: the stash made is not in the stash list")
}

// dropped is git stash drop's word for what it dropped: "Dropped
// refs/stash@{0} (<id>)".
var dropped = regexp.MustCompile(`\(([0-9a-f]{40,64})\)\s*$`)

// putBack checks that git stash drop, which said out, dropped the stash
// id, and when another was pushed between finding id and dropping it, so
// that the other was dropped, stores that one again.
func (q *Queue) putBack(ctx context.Context, git func(...string) (run.Result, error), id, out string) error {
	m := dropped.FindStringSubmatch(out)
	if m == nil {
		return fmt.Errorf("write: git stash drop said %q", strings.TrimSpace(out))
	}
	if m[1] == id {
		return nil
	}
	res, err := git("log", "-1", "--no-show-signature", "--format=%s", m[1], "--")
	if err == nil {
		_, err = git("stash", "store", "-m", strings.TrimSpace(string(res.Stdout)), m[1])
	}
	if err != nil {
		return fmt.Errorf("write: another stash, %s, was dropped and could not be stored again: %w", m[1], err)
	}
	return errors.New("write: another stash was pushed meanwhile; it is back in the stash list")
}

// Repeatable reports whether op can be submitted again as it is, after
// it failed: one that runs a command, not one the engine makes of
// several (undo and redo, snapshots), which are decided anew.
func (op Op) Repeatable() bool { return op.do == nil }

// Stashing is op, a switch, checkout or pull git refused because local
// changes would be overwritten, made to put them aside first. A pull
// takes them back after it, as git pull --autostash does; any other
// write leaves them stashed, as a switch that stashes does, and pops
// them back if it fails. False when op cannot be made so.
func Stashing(op Op) (Op, bool) {
	if !op.Repeatable() || op.Pre != nil || len(op.Cmd.Args) == 0 {
		return Op{}, false
	}
	switch op.Cmd.Args[0] {
	case "pull":
		if slices.Contains(op.Cmd.Args, "--autostash") {
			return Op{}, false
		}
		op.Cmd.Args = append([]string{"pull", "--autostash"}, op.Cmd.Args[1:]...)
	case "switch", "checkout":
		op.Pre = &run.Cmd{GitDir: op.Cmd.GitDir, WorkTree: op.Cmd.WorkTree, Timeout: HookTimeout,
			Args: []string{"stash", "push", "--include-untracked", "-m", preStashMark + op.Name}}
		op.PreStashes = true
	default:
		return Op{}, false
	}
	op.Name += ", stashing changes"
	return op, true
}
