package write

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"ramus/internal/git/run"
)

// popStash puts back the changes the write of e stashed before it ran,
// once its undo took HEAD back: the stash found by its commit, not as
// stash@{0}, which another may have been pushed over. When that fails,
// the error names the stash they stay in.
func (q *Queue) popStash(ctx context.Context, e Entry) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), unstashTimeout)
	defer cancel()
	workTree := e.op.Cmd.WorkTree
	entry := ""
	res, err := q.r.Run(ctx, &run.Cmd{GitDir: q.gitDir, WorkTree: workTree, Read: true,
		Args: []string{"log", "-g", "--format=%H", "refs/stash", "--"}})
	if err == nil {
		for n, line := range strings.Split(strings.TrimSpace(string(res.Stdout)), "\n") {
			if line == e.stash {
				entry = "stash@{" + strconv.Itoa(n) + "}"
				break
			}
		}
		if entry == "" {
			err = errors.New("write: the stash made is not in the stash list")
		}
	}
	if err == nil {
		_, err = q.r.Run(ctx, &run.Cmd{GitDir: q.gitDir, WorkTree: workTree, Timeout: unstashTimeout,
			Args: []string{"stash", "pop", "--index", entry}})
	}
	if err != nil {
		return &StashKeptError{Err: fmt.Errorf("write: undo of %s went back", e.Name), Entry: entry, Commit: e.stash, Pop: err}
	}
	return nil
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
