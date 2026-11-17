package write

import (
	"slices"

	"ramus/internal/git/run"
)

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
