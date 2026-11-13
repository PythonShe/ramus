package write

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"ramus/internal/git/run"
	"ramus/internal/git/snapshot"
)

// MaxPick bounds the commits one cherry-pick or revert takes.
const MaxPick = 1000

func checkCommits(commits []string) error {
	switch {
	case len(commits) == 0:
		return errors.New("write: no commits chosen")
	case len(commits) > MaxPick:
		return fmt.Errorf("%w: %d commits, at most %d", ErrTooMany, len(commits), MaxPick)
	}
	for _, c := range commits {
		if !isHex(c) {
			return fmt.Errorf("write: %q is not a commit id", c)
		}
	}
	return nil
}

func commitsName(verb string, commits []string) string {
	if len(commits) == 1 {
		return verb + " " + short(commits[0])
	}
	return fmt.Sprintf("%s %d commits", verb, len(commits))
}

// CherryPick applies commits (hex, oldest first) onto HEAD, each as a new
// commit. The branch is backed up first; a conflict leaves the
// cherry-pick in progress.
func CherryPick(w Where, commits []string, seen Seen) (Op, error) {
	if err := checkCommits(commits); err != nil {
		return Op{}, err
	}
	op := Op{Name: commitsName("Cherry-pick", commits), Undo: MovesHead, Expect: seen.expect(),
		Cmd: w.cmd(append([]string{"cherry-pick", "--end-of-options"}, commits...)...)}
	op.Cmd.Timeout = HookTimeout
	if seen.Branch != "" {
		op.Backup = []string{seen.Branch}
	}
	return op, nil
}

// Revert makes a commit undoing each of commits (hex, newest first), on
// HEAD. The branch is backed up first; a conflict leaves the revert in
// progress.
func Revert(w Where, commits []string, seen Seen) (Op, error) {
	if err := checkCommits(commits); err != nil {
		return Op{}, err
	}
	op := Op{Name: commitsName("Revert", commits), Undo: MovesHead, Expect: seen.expect(),
		Cmd: w.cmd(append([]string{"revert", "--no-edit", "--end-of-options"}, commits...)...)}
	op.Cmd.Timeout = HookTimeout
	if seen.Branch != "" {
		op.Backup = []string{seen.Branch}
	}
	return op, nil
}

// Clean deletes the untracked paths git clean listed, ignored ones too
// when ignored is set, after keeping them in a snapshot; undo brings them
// back.
func Clean(w Where, paths []string, ignored bool) (Op, error) {
	if len(paths) == 0 {
		return Op{}, errors.New("write: nothing to clean")
	}
	op, err := Delete(w, paths)
	if err != nil || !ignored {
		return op, err
	}
	op.Cmd.Args = append([]string{"clean", "-f", "-d", "-x", "-q", "--"}, paths...)
	op.Snapshot = &snapshot.Options{Reason: op.Name, Paths: paths, Ignored: true}
	return op, nil
}

// BisectVerdict is what the user says of a commit while bisecting.
type BisectVerdict string

const (
	BisectGood BisectVerdict = "good"
	BisectBad  BisectVerdict = "bad"
	BisectSkip BisectVerdict = "skip"
)

// BisectStart starts a bisect between a bad commit and good ones (hex);
// git checks out the first commit to test, refusing over local changes
// that would be overwritten.
func BisectStart(w Where, bad string, good []string, seen Seen) (Op, error) {
	if err := checkCommits(append([]string{bad}, good...)); err != nil {
		return Op{}, err
	}
	args := append([]string{"bisect", "start", bad}, good...)
	return Op{Name: "Start bisect", Expect: seen.expect(), Cmd: w.cmd(append(args, "--")...)}, nil
}

// BisectMark marks commit (hex), or HEAD when it is "", good, bad or
// skipped; git checks out the next commit to test.
func BisectMark(w Where, v BisectVerdict, commit string) (Op, error) {
	switch v {
	case BisectGood, BisectBad, BisectSkip:
	default:
		return Op{}, errors.New("write: unknown bisect verdict " + string(v))
	}
	args := []string{"bisect", string(v)}
	name := "Mark " + string(v)
	if commit != "" {
		if !isHex(commit) {
			return Op{}, fmt.Errorf("write: %q is not a commit id", commit)
		}
		args = append(args, commit)
		name += " " + short(commit)
	}
	return Op{Name: name, Cmd: w.cmd(args...)}, nil
}

// BisectReset ends a bisect and checks out where it started.
func BisectReset(w Where) Op {
	return Op{Name: "End bisect", Cmd: w.cmd("bisect", "reset")}
}

// ApplyPatch applies a patch file to the work tree. A mailbox of commits
// (what git format-patch writes) is applied as commits with git am,
// backing the branch up; a plain diff changes the files, which paths
// name, after keeping them in a snapshot. Either is all or nothing.
func ApplyPatch(w Where, file string, mailbox bool, paths []string, seen Seen) (Op, error) {
	if st, err := os.Stat(file); err != nil || !st.Mode().IsRegular() {
		return Op{}, fmt.Errorf("write: %s is not a file", file)
	}
	base := file[strings.LastIndexAny(file, `/\`)+1:]
	if mailbox {
		op := Op{Name: "Apply " + base + " as commits", Undo: MovesHead, Expect: seen.expect(),
			Cmd: run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: []string{"am", "--3way", "--", file}, Timeout: HookTimeout}}
		if seen.Branch != "" {
			op.Backup = []string{seen.Branch}
		}
		return op, nil
	}
	if len(paths) == 0 {
		return Op{}, errors.New("write: the patch changes no files")
	}
	return Op{Name: "Apply " + base, Undo: FilesOnly, Snapshot: &snapshot.Options{Reason: "Apply " + base, Paths: paths},
		Cmd: run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: []string{"apply", "--", file}}}, nil
}
