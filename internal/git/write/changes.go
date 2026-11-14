package write

import (
	"context"
	"errors"
	"fmt"
	"os"
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
	return Op{Name: "Stage " + count(len(paths), paths[0]), Cmd: w.cmd(fromStdin("add", "--all")...), Input: pathspecs(paths)}
}

// Unstage puts the paths in the index back as they are in HEAD; with
// unborn set (no commit yet) it takes them out of the index.
func Unstage(w Where, paths []string, unborn bool) Op {
	args := fromStdin("restore", "--staged")
	if unborn {
		args = fromStdin("rm", "--cached", "-r", "-q", "--ignore-unmatch")
	}
	return Op{Name: "Unstage " + count(len(paths), paths[0]), Cmd: w.cmd(args...), Input: pathspecs(paths)}
}

// Discard puts tracked paths in the work tree back as they are in the
// index, after keeping them in a snapshot; it is undoable.
func Discard(w Where, paths []string) Op {
	name := "Discard changes to " + count(len(paths), paths[0])
	return Op{Name: name, Cmd: w.cmd(fromStdin("restore", "--worktree")...), Input: pathspecs(paths),
		Snapshot: &snapshot.Options{Reason: name, Paths: paths}, Undo: FilesOnly}
}

// MaxArgPaths bounds the paths a write passes as arguments in one
// command, where git reads none from standard input.
const MaxArgPaths = 1000

// ErrTooMany is more paths than one write takes.
var ErrTooMany = errors.New("write: too many paths at once")

// maxArgBytes bounds the paths one git clean names: git clean reads no
// paths from standard input, and Windows limits a command line to 32 K
// characters.
const maxArgBytes = 24 << 10

// Delete removes untracked paths, folders included, after keeping them
// in a snapshot; it is undoable. git clean takes paths only as
// arguments, so many paths take several runs, inside the one write.
func Delete(w Where, paths []string) (Op, error) {
	if len(paths) == 0 {
		return Op{}, errors.New("write: no files chosen")
	}
	name := "Delete " + count(len(paths), paths[0])
	var cmds []run.Cmd
	for rest := paths; len(rest) > 0; {
		args := []string{"clean", "-f", "-d", "-q", "--"}
		head, n := len(args), 0
		for i, p := range rest {
			if i > 0 && n+len(p)+1 > maxArgBytes {
				break
			}
			args, n = append(args, p), n+len(p)+1
		}
		rest = rest[len(args)-head:]
		cmds = append(cmds, w.cmd(args...))
	}
	return Op{Name: name, Cmd: cmds[0], More: cmds[1:],
		Snapshot: &snapshot.Options{Reason: name, Paths: paths}, Undo: FilesOnly}, nil
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
