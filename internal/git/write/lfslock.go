package write

import (
	"errors"
	"strings"

	"ramus/internal/git/run"
)

// LFSLock locks a file on the repository's LFS server, so no one else
// can push a change to it until it is unlocked. path is relative to the
// work tree.
func LFSLock(w Where, path string) (Op, error) {
	if err := lfsPath(path); err != nil {
		return Op{}, err
	}
	return Op{Name: "Lock " + path, RefsOnly: true,
		Cmd: run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: []string{"lfs", "lock", "--", path},
			Timeout: PushTimeout, Stall: NetworkStall}}, nil
}

// LFSUnlock releases the lock with id on path. force releases a lock
// someone else holds, and one on a file with changes not committed,
// which git-lfs otherwise refuses.
func LFSUnlock(w Where, path, id string, force bool) (Op, error) {
	if id == "" || strings.HasPrefix(id, "-") {
		return Op{}, errors.New("write: no lock chosen")
	}
	args := []string{"lfs", "unlock", "--id=" + id}
	name := "Unlock " + path
	if force {
		args = append(args, "--force")
		name = "Force unlock " + path
	}
	return Op{Name: name, RefsOnly: true,
		Cmd: run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: args, Timeout: PushTimeout, Stall: NetworkStall}}, nil
}

func lfsPath(path string) error {
	if path == "" {
		return errors.New("write: no file chosen")
	}
	return nil
}
