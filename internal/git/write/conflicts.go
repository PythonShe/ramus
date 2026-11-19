package write

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"ramus/internal/git/osfile"
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
// A path the chosen side deleted is resolved by RemoveConflicted. A
// submodule is resolved with the commit that side records, whatever
// commit the submodule has checked out.
func TakeSide(w Where, paths []string, side Side) (Op, error) {
	if err := checkPaths(paths); err != nil {
		return Op{}, err
	}
	flag, stage, name := "--ours", "2", "Take ours for "
	if side == TheirSide {
		flag, stage, name = "--theirs", "3", "Take theirs for "
	}
	name += count(len(paths), paths[0])
	op := Op{Name: name, Snapshot: &snapshot.Options{Reason: name, Paths: paths},
		Cmd: w.cmd(fromStdin("add")...), Input: pathspecs(paths)}
	// The index is read in the job's turn: which paths are submodules is
	// what the conflict holds when the write runs.
	op.do = func(ctx context.Context, j *Job) (run.Result, error) {
		if err := j.q.prepare(ctx, j); err != nil {
			return run.Result{}, err
		}
		links, files, err := gitlinks(ctx, j.q.r, w, paths, stage)
		if err != nil {
			return run.Result{}, err
		}
		var res run.Result
		if len(files) > 0 {
			if res, err = execute(ctx, j.q.r, j, w.cmd(fromStdin("checkout", flag)...), pathspecs(files)); err != nil {
				return res, err
			}
			if res, err = execute(ctx, j.q.r, j, w.cmd(fromStdin("add")...), pathspecs(files)); err != nil {
				return res, err
			}
		}
		if len(links) > 0 {
			// git checkout leaves a gitlink as it is and git add stages
			// the commit the submodule has checked out, so the side's
			// entry is staged as it is.
			res, err = execute(ctx, j.q.r, j, w.cmd("update-index", "-z", "--index-info"), links)
		}
		return res, err
	}
	return op, nil
}

// gitlinks splits conflicted paths into the submodules whose entry at
// stage (2 ours, 3 theirs) the index holds, as lines for git update-index
// --index-info -z that stage it resolved, and the other paths.
func gitlinks(ctx context.Context, r *run.Runner, w Where, paths []string, stage string) ([]byte, []string, error) {
	res, err := r.Run(ctx, &run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: []string{"ls-files", "-u", "-z"}, Read: true})
	if err != nil {
		return nil, nil, err
	}
	link := map[string]string{}
	for _, e := range strings.Split(string(res.Stdout), "\x00") {
		// <mode> <oid> <stage>\t<path>
		info, path, ok := strings.Cut(e, "\t")
		f := strings.Fields(info)
		if ok && len(f) == 3 && f[0] == "160000" && f[2] == stage {
			link[path] = f[1]
		}
	}
	var lines []byte
	var files []string
	for _, p := range paths {
		if oid, ok := link[strings.TrimSuffix(p, "/")]; ok {
			lines = fmt.Appendf(lines, "160000 %s\t%s\x00", oid, strings.TrimSuffix(p, "/"))
		} else {
			files = append(files, p)
		}
	}
	return lines, files, nil
}

// RemoveConflicted resolves conflicted paths by deleting them, as when
// the side kept deleted them; they are kept in a snapshot first.
func RemoveConflicted(w Where, paths []string) (Op, error) {
	if err := checkPaths(paths); err != nil {
		return Op{}, err
	}
	name := "Resolve by deleting " + count(len(paths), paths[0])
	return Op{Name: name, Snapshot: &snapshot.Options{Reason: name, Paths: paths},
		Cmd: w.cmd(fromStdin("rm", "-q", "--ignore-unmatch")...), Input: pathspecs(paths)}, nil
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
// message git prepared, every line of it kept but the conflicts git
// listed in it. Skipping and aborting drop work, which is kept in a
// snapshot first.
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
	if action == Continue && operation != "am" {
		op.do = func(ctx context.Context, j *Job) (run.Result, error) {
			cmd := j.Op.Cmd
			cc, err := keepMessage(w.GitDir)
			if err != nil {
				return run.Result{}, err
			}
			if cc != "" {
				cmd.Config = append(cmd.Config, "core.commentChar="+cc)
			}
			return execute(ctx, j.q.r, j, cmd, nil)
		}
	}
	return op, nil
}

// maxMessage bounds a message file of the git directory read here; git
// writes none near it.
const maxMessage = 16 << 20

// keepMessage readies the message git prepared for the commit a continue
// makes (MERGE_MSG, and a rebase's own copy) to keep every line: the
// conflicts git listed in it, as comments, are taken out, and the comment
// character returned is one no line left starts with, as git's editor,
// even one that changes nothing, strips comments. "" leaves the message
// to git: in a squash, git's own comments join the messages, and the
// character returned is the one they start with.
func keepMessage(gitDir string) (string, error) {
	if b, err := osfile.ReadFile(filepath.Join(gitDir, "rebase-merge", "current-fixups"), maxMessage); err == nil && len(bytes.TrimSpace(b)) > 0 {
		return squashComment(gitDir), nil
	}
	var all strings.Builder
	for _, name := range []string{"MERGE_MSG", filepath.Join("rebase-merge", "message")} {
		p := filepath.Join(gitDir, name)
		b, err := osfile.ReadFile(p, maxMessage)
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, osfile.ErrTooLarge) {
			// Past the bound, the message is left to git as it is.
			continue
		}
		if err != nil {
			return "", err
		}
		msg := dropConflicts(string(b))
		if msg != string(b) {
			if err := os.WriteFile(p, []byte(msg), 0o644); err != nil {
				return "", err
			}
		}
		all.WriteString(msg)
	}
	// With every character taken, git strips as it always did.
	cc, _ := commentChar(all.String())
	return cc, nil
}

// PreparedMessage is the message git prepared in MERGE_MSG for the
// commit that ends a merge, cherry-pick or revert stopped, as git's
// editor would show it stripped: without the conflicts listed and lines
// starting with comment, the comment string. "" when there is none.
func PreparedMessage(gitDir, comment string) (string, error) {
	b, err := osfile.ReadFile(filepath.Join(gitDir, "MERGE_MSG"), maxMessage)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var kept []string
	for _, l := range strings.Split(dropConflicts(string(b)), "\n") {
		if comment != "" && strings.HasPrefix(l, comment) {
			continue
		}
		kept = append(kept, strings.TrimRight(l, " \t\r"))
	}
	return strings.TrimSpace(strings.Join(kept, "\n")), nil
}

// squashComment is the comment string git wrote a squash's message with,
// its first line a comment: one chosen by an earlier continue of the
// same rebase, which git keeps using, strips only if given again.
func squashComment(gitDir string) string {
	for _, name := range []string{filepath.Join("rebase-merge", "message"), "MERGE_MSG"} {
		b, err := osfile.ReadFile(filepath.Join(gitDir, name), maxMessage)
		if err != nil {
			continue
		}
		first, _, _ := strings.Cut(string(b), "\n")
		c, _, ok := strings.Cut(first, " ")
		if ok && isMarks(c) && strings.Contains(string(b), "\n"+c+" ") {
			return c
		}
	}
	return ""
}

// isMarks reports whether c could be a comment string: marks, not a
// word.
func isMarks(c string) bool {
	return c != "" && strings.IndexFunc(c, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsLetter(r) || unicode.IsDigit(r) }) < 0
}

// dropConflicts takes the list of conflicted paths git ends a message
// with off it: "<c> Conflicts:" and a line "<c>\t<path>" for each, after
// the scissors line when commit.cleanup is scissors.
func dropConflicts(msg string) string {
	lines := strings.SplitAfter(msg, "\n")
	i := len(lines) - 1
	for i >= 0 && !strings.HasSuffix(strings.TrimRight(lines[i], "\n"), " Conflicts:") {
		i--
	}
	if i < 0 {
		return msg
	}
	c := strings.TrimSuffix(strings.TrimRight(lines[i], "\n"), " Conflicts:")
	// A comment string is marks, not a word: a line of the user's own,
	// "Resolved Conflicts:", is not git's.
	if !isMarks(c) {
		return msg
	}
	paths := 0
	for _, l := range lines[i+1:] {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if !strings.HasPrefix(l, c+"\t") {
			return msg
		}
		paths++
	}
	if paths == 0 {
		return msg // git lists at least one
	}
	kept := strings.Join(lines[:i], "")
	if cut := strings.LastIndex(kept, c+" ------------------------ >8 ------------------------\n"); cut >= 0 && (cut == 0 || kept[cut-1] == '\n') {
		kept = kept[:cut]
	}
	return strings.TrimRight(kept, "\n") + "\n"
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
	cmd := w.cmd(append(args, "--")...)
	cmd.Timeout = MergeToolTime
	// git mergetool reads no paths from standard input; it takes the
	// files one at a time, so several runs do as one would.
	cmds := SplitArgs(cmd, paths)
	return Op{Name: name, Snapshot: &snapshot.Options{Reason: name, Paths: paths}, Cmd: cmds[0], More: cmds[1:]}, nil
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
