package repo

import (
	"os"
	"path/filepath"
)

// Operation is a multi-step git operation left in progress in a work
// tree, which the app shows with its continue, skip and abort actions.
type Operation string

const (
	NoOperation Operation = ""
	Merge       Operation = "merge"
	Rebase      Operation = "rebase"
	Am          Operation = "am"
	CherryPick  Operation = "cherry-pick"
	Revert      Operation = "revert"
	Bisect      Operation = "bisect"
)

// InProgress reads which operation is in progress in the work tree whose
// git directory is gitDir, from the files git leaves there, in the order
// git's own status checks them. Bisect can run alongside the others; it
// is reported only when nothing else is in progress.
func InProgress(gitDir string) Operation {
	has := func(name string) bool {
		_, err := os.Lstat(filepath.Join(gitDir, name))
		return err == nil
	}
	switch {
	case has("rebase-merge"):
		return Rebase
	case has("rebase-apply/applying"):
		return Am
	case has("rebase-apply"):
		return Rebase
	case has("MERGE_HEAD"):
		return Merge
	case has("CHERRY_PICK_HEAD"):
		return CherryPick
	case has("REVERT_HEAD"):
		return Revert
	case has("sequencer/todo"):
		// A cherry-pick or revert of several commits stopped between two.
		if data, err := os.ReadFile(filepath.Join(gitDir, "sequencer", "todo")); err == nil && len(data) >= 6 && string(data[:6]) == "revert" {
			return Revert
		}
		return CherryPick
	case has("BISECT_LOG"):
		return Bisect
	}
	return NoOperation
}
