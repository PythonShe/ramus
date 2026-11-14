package write

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"ramus/internal/git/pathsafe"
	"ramus/internal/git/run"
	"ramus/internal/git/snapshot"
)

// FileWrite is a file the app writes into the work tree itself.
type FileWrite struct {
	Path string // relative to the work tree
	Data []byte
}

// write replaces the file whole or not at all, keeping its mode; a path
// that leads out of the work tree is refused.
func (f *FileWrite) write(workTree string) error {
	p, err := pathsafe.Target(workTree, f.Path)
	if err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if st, err := os.Stat(p); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".ramus-merge-*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(f.Data)
	if err == nil {
		err = tmp.Chmod(mode)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), p)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

// Side is one side of a conflict.
type Side uint8

const (
	OurSide Side = iota
	TheirSide
)

// TakeSide resolves conflicted paths with one side's version whole, and
// marks them resolved. The conflicted files are kept in a snapshot first.
// A path the chosen side deleted is resolved by RemoveConflicted.
func TakeSide(w Where, paths []string, side Side) (Op, error) {
	if err := checkPaths(paths); err != nil {
		return Op{}, err
	}
	flag, name := "--ours", "Take ours for "
	if side == TheirSide {
		flag, name = "--theirs", "Take theirs for "
	}
	name += count(len(paths), paths[0])
	pre := w.cmd(append([]string{"checkout", flag, "--"}, paths...)...)
	return Op{Name: name, Pre: &pre, Snapshot: &snapshot.Options{Reason: name, Paths: paths},
		Cmd: w.cmd(append([]string{"add", "--"}, paths...)...)}, nil
}

// RemoveConflicted resolves conflicted paths by deleting them, as when
// the side kept deleted them; they are kept in a snapshot first.
func RemoveConflicted(w Where, paths []string) (Op, error) {
	if err := checkPaths(paths); err != nil {
		return Op{}, err
	}
	name := "Resolve by deleting " + count(len(paths), paths[0])
	return Op{Name: name, Snapshot: &snapshot.Options{Reason: name, Paths: paths},
		Cmd: w.cmd(append([]string{"rm", "-q", "--ignore-unmatch", "--"}, paths...)...)}, nil
}

// Resolve writes the merged result of a conflicted file and marks it
// resolved, refusing if the file changed since it was read (seen). The
// file with its markers is kept in a snapshot first.
func Resolve(w Where, path string, data []byte, seen FileState) (Op, error) {
	if err := checkPaths([]string{path}); err != nil {
		return Op{}, err
	}
	name := "Resolve " + path
	return Op{Name: name, Expect: &Expect{AnyHead: true, Files: []FileState{seen}},
		Snapshot: &snapshot.Options{Reason: name, Paths: []string{path}},
		File:     &FileWrite{Path: path, Data: data},
		Cmd:      w.cmd("add", "--", path)}, nil
}

func checkPaths(paths []string) error {
	switch {
	case len(paths) == 0:
		return errors.New("write: no files chosen")
	case len(paths) > MaxArgPaths:
		return fmt.Errorf("%w: %d files, at most %d", ErrTooMany, len(paths), MaxArgPaths)
	}
	return nil
}

// SequenceAction is what to do with an operation stopped part way.
type SequenceAction string

const (
	Continue SequenceAction = "continue"
	Skip     SequenceAction = "skip"
	Abort    SequenceAction = "abort"
)

// Sequence continues, skips or aborts the operation in progress: "merge",
// "rebase", "cherry-pick", "revert" or "am". Continuing takes the commit
// message git prepared. Skipping and aborting drop work, which is kept in
// a snapshot first.
func Sequence(w Where, operation string, action SequenceAction) (Op, error) {
	switch operation {
	case "merge", "rebase", "cherry-pick", "revert", "am":
	default:
		return Op{}, errors.New("write: no " + operation + " to go on with")
	}
	switch {
	case action == Skip && operation == "merge":
		return Op{}, errors.New("write: a merge cannot skip")
	case action != Continue && action != Skip && action != Abort:
		return Op{}, errors.New("write: unknown action " + string(action))
	}
	name := map[SequenceAction]string{Continue: "Continue ", Skip: "Skip in ", Abort: "Abort "}[action] + operation
	op := Op{Name: name, Cmd: run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: []string{operation, "--" + string(action)},
		Timeout: HookTimeout, Env: []string{"GIT_EDITOR=true"}}}
	if action != Continue {
		op.Snapshot = &snapshot.Options{Reason: name}
	}
	return op, nil
}

// MergeToolTime bounds how long an external merge tool stays open.
const MergeToolTime = 4 * time.Hour

// MergeTool hands conflicted paths to the merge tool git's config names,
// through git mergetool, which marks each resolved as the tool closes.
// The queue waits for it.
func MergeTool(w Where, paths []string, gui bool) (Op, error) {
	if err := checkPaths(paths); err != nil {
		return Op{}, err
	}
	args := []string{"mergetool", "--no-prompt"}
	if gui {
		args = append(args, "--gui")
	}
	name := "Merge tool for " + count(len(paths), paths[0])
	cmd := w.cmd(append(append(args, "--"), paths...)...)
	cmd.Timeout = MergeToolTime
	return Op{Name: name, Snapshot: &snapshot.Options{Reason: name, Paths: paths}, Cmd: cmd}, nil
}

// ObserveMerged records the MERGED file git mergetool hands over,
// absolute, for SaveMerged to check it is unchanged.
func ObserveMerged(path string) FileState {
	dir, name := filepath.Split(path)
	return stat(dir, name)
}

// SaveMerged writes a merge resolved for git mergetool to its MERGED file,
// absolute, whole or not at all, and refuses when the file changed since
// seen. git mergetool stages it once the tool exits.
func SaveMerged(path string, data []byte, seen FileState) error {
	dir, name := filepath.Split(path)
	if now := stat(dir, name); !now.same(seen) {
		return &StaleError{What: name, Saw: describe(seen), Now: describe(now)}
	}
	return (&FileWrite{Path: name, Data: data}).write(dir)
}
