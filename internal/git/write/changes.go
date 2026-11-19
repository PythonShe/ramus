package write

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"ramus/internal/git/pathsafe"
	"ramus/internal/git/run"
	"ramus/internal/git/snapshot"
)

// Where names the repository a change is made in.
type Where struct {
	GitDir, WorkTree string
}

// pathspecs is paths as git reads them from standard input: NUL after
// each, taken literally.
func pathspecs(paths []string) []byte {
	var b []byte
	for _, p := range paths {
		b = append(append(b, p...), 0)
	}
	return b
}

func (w Where) cmd(args ...string) run.Cmd {
	return run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: args, Env: []string{"GIT_LITERAL_PATHSPECS=1"}}
}

// slow is cmd for a command that runs the clean or smudge filters, Git
// LFS among them, or the post-checkout hook, which take their time.
func (w Where) slow(args ...string) run.Cmd {
	c := w.cmd(args...)
	c.Timeout = HookTimeout
	return c
}

func fromStdin(args ...string) []string {
	return append(args, "--pathspec-from-file=-", "--pathspec-file-nul")
}

func count(n int, one string) string {
	if n == 1 {
		return one
	}
	return fmt.Sprintf("%d files", n)
}

// Stage adds the paths as they are in the work tree to the index,
// deletions included.
func Stage(w Where, paths []string) Op {
	return Op{Name: "Stage " + count(len(paths), paths[0]), Cmd: w.slow(fromStdin("add", "--all")...), Input: pathspecs(paths)}
}

// Unstage puts the paths in the index back as they are in HEAD; with
// unborn set (no commit yet) it takes them out of the index.
func Unstage(w Where, paths []string, unborn bool) Op {
	return unstageOp(w, paths, unborn, false)
}

// unstageOp is Unstage; force takes a file out of the index of an unborn
// repository though the index holds content the file no longer has,
// which git rm refuses to lose unless the write kept it first.
func unstageOp(w Where, paths []string, unborn, force bool) Op {
	args := fromStdin("restore", "--staged")
	if unborn {
		args = fromStdin("rm", "--cached", "-r", "-q", "--ignore-unmatch")
		if force {
			args = append(args, "-f")
		}
	}
	return Op{Name: "Unstage " + count(len(paths), paths[0]), Cmd: w.cmd(args...), Input: pathspecs(paths)}
}

// noRecurse keeps a command that puts files back out of submodules,
// whatever submodule.recurse says: the snapshot taken first holds a
// submodule's commit, not the files inside it.
const noRecurse = "--no-recurse-submodules"

// Discard puts tracked paths in the work tree back as they are in the
// index, after keeping them in a snapshot; it is undoable.
func Discard(w Where, paths []string) Op {
	name := "Discard changes to " + count(len(paths), paths[0])
	return Op{Name: name, Cmd: w.slow(fromStdin("restore", "--worktree", noRecurse)...), Input: pathspecs(paths),
		Snapshot: &snapshot.Options{Reason: name, Paths: paths}, Undo: FilesOnly}
}

// RestoreFrom puts path in the work tree back as it is in commit (hex),
// after keeping it in a snapshot; it is undoable. The index is left as
// it is, so the change shows unstaged, to be read before it is taken. A
// path commit does not have is removed, as it was then.
func RestoreFrom(w Where, path, commit string) (Op, error) {
	path = strings.TrimSuffix(path, "/")
	if path == "" {
		return Op{}, errors.New("write: no file chosen")
	}
	if !isHex(commit) {
		return Op{}, fmt.Errorf("write: %q is not a commit id", commit)
	}
	name := "Restore " + path + " as of " + short(commit)
	return Op{Name: name, Cmd: w.slow(fromStdin("restore", "--source="+commit, "--worktree", "--no-overlay", noRecurse)...),
		Input: pathspecs([]string{path}), Snapshot: &snapshot.Options{Reason: name, Paths: []string{path}, Writes: true, Source: commit}, Undo: FilesOnly}, nil
}

// MaxArgPaths bounds the paths one write on conflicted files takes.
const MaxArgPaths = 1000

// ErrTooMany is more paths than one write takes.
var ErrTooMany = errors.New("write: too many paths at once")

// Delete removes untracked paths, folders included, after keeping them
// in a snapshot; it is undoable. git clean takes paths only as
// arguments, so many paths take several runs, inside the one write.
func Delete(w Where, paths []string) (Op, error) {
	if len(paths) == 0 {
		return Op{}, errors.New("write: no files chosen")
	}
	name := "Delete " + count(len(paths), paths[0])
	cmds := argCmds(w.cmd, []string{"clean", "-f", "-d", "-q", "--"}, paths)
	return Op{Name: name, Cmd: cmds[0], More: cmds[1:],
		Snapshot: &snapshot.Options{Reason: name, Paths: paths}, Undo: FilesOnly}, nil
}

// argCmds runs args with paths after them, as many runs of mk as keep
// each command line short enough.
func argCmds(mk func(...string) run.Cmd, args, paths []string) []run.Cmd {
	return SplitArgs(mk(args...), paths)
}

// SplitArgs is c with paths after its arguments, as many copies of it as
// keep each command line within what every platform takes, for a git
// command that reads no paths from standard input; none for no paths.
// Each copy runs on its own, so a command that reads all its paths as
// one whole (a diff that pairs renames) sees each part alone.
func SplitArgs(c run.Cmd, paths []string) []run.Cmd {
	base := c.LineBytes()
	var cmds []run.Cmd
	for rest := paths; len(rest) > 0; {
		n, k := base, 0
		for k < len(rest) && (k == 0 || n+run.ArgBytes(rest[k]) <= run.MaxArgBytes) {
			n += run.ArgBytes(rest[k])
			k++
		}
		part := c
		part.Args = append(slices.Clip(c.Args), rest[:k]...)
		cmds = append(cmds, part)
		rest = rest[k:]
	}
	return cmds
}

// DiscardStaged discards changes staged and not: it unstages the paths
// of unstage, puts tracked back as they are in the index then, and
// deletes untracked, as Unstage, Discard and Delete do, in one write
// that keeps every path in a snapshot first, the index with them: what
// was staged is kept too, and undo puts it back. unborn is a repository
// with no commit yet.
func DiscardStaged(w Where, unstage, tracked, untracked []string, unborn bool) (Op, error) {
	all := slices.Compact(slices.Sorted(slices.Values(slices.Concat(unstage, tracked, untracked))))
	if len(unstage) == 0 || len(all) == 0 {
		return Op{}, errors.New("write: no files chosen")
	}
	name := "Discard changes to " + count(len(all), all[0])
	// The snapshot keeps what the index held, which git rm need not
	// refuse to lose.
	op := unstageOp(w, unstage, unborn, true)
	op.Name = name
	// A file staged as new is deleted though ignored, as one added with
	// git add -f is once unstaged: -x for those files alone, not for the
	// ignored files of an untracked folder, which the snapshot does not
	// keep.
	staged := map[string]bool{}
	for _, p := range unstage {
		staged[p] = true
	}
	var added, loose []string
	for _, p := range untracked {
		if staged[p] {
			added = append(added, p)
		} else {
			loose = append(loose, p)
		}
	}
	op.More = slices.Concat(argCmds(w.slow, []string{"restore", "--worktree", noRecurse, "--"}, tracked),
		argCmds(w.cmd, []string{"clean", "-f", "-d", "-x", "-q", "--"}, added),
		argCmds(w.cmd, []string{"clean", "-f", "-d", "-q", "--"}, loose))
	op.Snapshot = &snapshot.Options{Reason: name, Paths: all}
	op.Undo = FilesOnly
	return op, nil
}

// Partial is a patch made by diff.Parsed.Select, and what to do with it.
type Partial struct {
	Path  string
	Patch []byte
	// Cached applies it to the index; otherwise to the work tree.
	Cached bool
	// Reverse applies it with -R.
	Reverse bool
}

// Apply writes a partial patch: staging lines (Cached), unstaging them
// (Cached, Reverse), or discarding them from the work tree (Reverse),
// which keeps a snapshot first and is undoable.
func Apply(w Where, p Partial) Op {
	args := []string{"apply", "--whitespace=nowarn"}
	name := "Stage lines of "
	if p.Cached {
		args = append(args, "--cached")
	}
	if p.Reverse {
		args = append(args, "-R")
		name = "Unstage lines of "
	}
	args = append(args, "-")
	op := Op{Cmd: w.cmd(args...), Input: p.Patch}
	if !p.Cached {
		name = "Discard lines of "
		op.Snapshot = &snapshot.Options{Reason: name + p.Path, Paths: []string{p.Path}}
		op.Undo = FilesOnly
	}
	op.Name = name + p.Path
	return op
}

// Ignore adds a line to the .gitignore at the top of the work tree,
// creating it when there is none. pattern is written as it is; see
// IgnorePattern for a path.
func Ignore(w Where, pattern string) (Op, error) {
	if pattern == "" || strings.ContainsAny(pattern, "\n\r") {
		return Op{}, fmt.Errorf("write: %q is not a pattern for .gitignore", pattern)
	}
	return Op{Name: "Ignore " + pattern, Cmd: w.cmd(), do: func(ctx context.Context, _ *Job) (run.Result, error) {
		return run.Result{}, appendIgnore(w.WorkTree, pattern)
	}}, nil
}

func appendIgnore(root, pattern string) error {
	p, err := pathsafe.InWorkTree(root, ".gitignore")
	if err != nil {
		return err
	}
	if st, err := os.Lstat(p); err == nil && !st.Mode().IsRegular() {
		return fmt.Errorf("write: %s is not a regular file", p)
	}
	old, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	line := pattern + "\n"
	if len(old) > 0 && old[len(old)-1] != '\n' {
		line = "\n" + line
	}
	if _, err := f.WriteString(line); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// IgnorePattern is the .gitignore line that matches exactly one path from
// the top of the work tree: anchored with a leading slash (so a leading #
// or ! is not read as a comment or a negation), with wildcards and
// trailing spaces escaped.
func IgnorePattern(path string) string {
	var b strings.Builder
	b.WriteByte('/')
	for _, r := range path {
		if strings.ContainsRune(`*?[\`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	s := b.String()
	// Trailing spaces are dropped unless escaped.
	if t := strings.TrimRight(s, " "); len(t) < len(s) {
		s = t + strings.Repeat(`\ `, len(s)-len(t))
	}
	return s
}
