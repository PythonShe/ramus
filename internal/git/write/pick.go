package write

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
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
	return pick(w, "Cherry-pick", []string{"cherry-pick"}, commits, 0, seen)
}

// CherryPickMerge applies merge commit (hex) onto HEAD as one new commit:
// what it changed against its parent number mainline, 1 for the first.
// git takes no merge without it.
func CherryPickMerge(w Where, commit string, mainline int, seen Seen) (Op, error) {
	return pick(w, "Cherry-pick", []string{"cherry-pick"}, []string{commit}, mainline, seen)
}

// Revert makes a commit undoing each of commits (hex, newest first), on
// HEAD. The branch is backed up first; a conflict leaves the revert in
// progress.
func Revert(w Where, commits []string, seen Seen) (Op, error) {
	return pick(w, "Revert", []string{"revert", "--no-edit"}, commits, 0, seen)
}

// RevertMerge makes a commit undoing merge commit (hex), on HEAD: what
// it brought in, as against its parent number mainline, the side kept.
func RevertMerge(w Where, commit string, mainline int, seen Seen) (Op, error) {
	return pick(w, "Revert", []string{"revert", "--no-edit"}, []string{commit}, mainline, seen)
}

// pick runs args, git cherry-pick or revert, on commits; mainline, when
// above 0, is the parent of the one merge commit that its change is
// taken against.
func pick(w Where, verb string, args, commits []string, mainline int, seen Seen) (Op, error) {
	if err := checkCommits(commits); err != nil {
		return Op{}, err
	}
	if mainline < 0 || mainline > 0 && len(commits) != 1 {
		return Op{}, fmt.Errorf("write: parent %d of %d commits", mainline, len(commits))
	}
	if mainline > 0 {
		args = append(args, "--mainline", strconv.Itoa(mainline))
	}
	// The commits go on standard input: a thousand of them pass what
	// Windows takes on a command line.
	op := Op{Name: commitsName(verb, commits), Undo: MovesHead, Expect: seen.expect(),
		Cmd: w.cmd(append(args, "--stdin")...), Input: []byte(strings.Join(commits, "\n") + "\n")}
	op.Cmd.Timeout = HookTimeout
	if seen.Branch != "" {
		op.Backup = []string{seen.Branch}
	}
	return op, nil
}

// Clean deletes the untracked paths git clean listed, ignored ones too
// when ignored is set, after keeping them in a snapshot; undo brings them
// back. seen is the paths as they were listed (Observe): the clean
// refuses if one changed since.
func Clean(w Where, paths []string, ignored bool, seen []FileState) (Op, error) {
	if len(paths) == 0 {
		return Op{}, errors.New("write: nothing to clean")
	}
	op, err := Delete(w, paths)
	if err != nil {
		return op, err
	}
	op.Expect = &Expect{AnyHead: true, Files: seen}
	if !ignored {
		return op, nil
	}
	cmds := argCmds(w.cmd, []string{"clean", "-f", "-d", "-x", "-q", "--"}, paths)
	op.Cmd, op.More = cmds[0], cmds[1:]
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
	return Op{Name: "Start bisect", Expect: seen.expect(), Cmd: w.slow(append(args, "--")...)}, nil
}

// BisectTerm is the word that marks a commit v in a bisect started with
// terms of its own, newTerm for bad and oldTerm for good, as git bisect
// start --term-new and --term-old set them: git bisect bad and good
// refuse such a bisect. Empty terms are bad and good.
func BisectTerm(v BisectVerdict, newTerm, oldTerm string) BisectVerdict {
	switch {
	case v == BisectBad && newTerm != "":
		return BisectVerdict(newTerm)
	case v == BisectGood && oldTerm != "":
		return BisectVerdict(oldTerm)
	}
	return v
}

// bisectCommands are the words git bisect takes as commands, which it
// refuses as terms.
var bisectCommands = []string{"help", "start", "skip", "next", "reset", "visualize", "view", "replay", "log", "run", "terms"}

// validTerm reports a word git could have taken as a bisect's term: one
// it keeps in a ref's name, refs/bisect/<term>, and reads as no option.
func validTerm(t string) bool {
	if t == "" || t[0] == '-' || t[0] == '.' || strings.HasSuffix(t, ".lock") || strings.Contains(t, "..") ||
		strings.ContainsAny(t, " ~^:?*[\\/@{\x7f") || slices.Contains(bisectCommands, t) {
		return false
	}
	for _, r := range t {
		if r < ' ' {
			return false
		}
	}
	return true
}

// BisectMark marks commit (hex), or HEAD when it is "", good, bad or
// skipped, or with one of the bisect's own terms (BisectTerm); git checks
// out the next commit to test once there are a good and a bad one. start
// starts the bisect first.
func BisectMark(w Where, v BisectVerdict, commit string, start bool) (Op, error) {
	switch {
	case v == BisectGood, v == BisectBad, v == BisectSkip:
	case !start && validTerm(string(v)):
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
	op := Op{Name: name, Cmd: w.slow(args...)}
	if start {
		op.Name = "Start bisect, " + strings.ToLower(name[:1]) + name[1:]
		op.Pre = &run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: []string{"bisect", "start"}, Timeout: HookTimeout}
	}
	return op, nil
}

// BisectMarkTesting marks commit, the one the bisect had under test when
// the user chose v, as BisectMark does; it refuses once the bisect moved
// on to another, as a mark queued behind one that moved it does.
func BisectMarkTesting(w Where, v BisectVerdict, commit string) (Op, error) {
	op, err := BisectMark(w, v, commit, false)
	if err == nil && commit != "" {
		op.Expect = &Expect{AnyHead: true, Testing: commit}
	}
	return op, err
}

// BisectReset ends a bisect and checks out where it started.
func BisectReset(w Where) Op {
	return Op{Name: "End bisect", Cmd: w.slow("bisect", "reset")}
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
		Cmd: run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: []string{"apply", "--", file}, Timeout: HookTimeout}}, nil
}
