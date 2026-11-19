package diff

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"

	"ramus/internal/git/run"
)

// Lines is how many lines a change adds to a file and deletes from it.
// The zero Lines is a file not counted.
type Lines struct {
	Added, Deleted uint32
	// Binary is set for a file git does not count by lines.
	Binary bool
	// Known is set once the file is counted.
	Known bool
}

// Add adds o's counts to l; a file not counted adds nothing.
func (l *Lines) Add(o Lines) {
	if !o.Known || o.Binary {
		return
	}
	l.Added += o.Added
	l.Deleted += o.Deleted
	l.Known = true
}

// Total adds up ls.
func Total(ls []Lines) Lines {
	var t Lines
	for _, l := range ls {
		t.Add(l)
	}
	return t
}

// NumStat is the line counts of a diff as git diff --numstat -z prints
// them, in flat storage: each file by its old and new path, which are
// the same but for a rename or copy.
type NumStat struct {
	paths []byte
	ends  []uint32 // two per entry: old path end, new path end
	lines []Lines
	// order holds the entries sorted by new path, then old, for Find.
	order []uint32
}

// Len returns the number of entries.
func (n *NumStat) Len() int { return len(n.lines) }

func (n *NumStat) path(k int) []byte {
	start := uint32(0)
	if k > 0 {
		start = n.ends[k-1]
	}
	return n.paths[start:n.ends[k]:n.ends[k]]
}

func (n *NumStat) compare(i int, old, new []byte) int {
	if c := bytes.Compare(n.path(2*i+1), new); c != 0 {
		return c
	}
	return bytes.Compare(n.path(2*i), old)
}

// Find returns the counts of the file at new that was at old before; a
// file not changed in place has the same path on both sides. A file the
// diff does not hold is not counted.
func (n *NumStat) Find(old, new []byte) Lines {
	if n == nil {
		return Lines{}
	}
	k, ok := slices.BinarySearchFunc(n.order, 0, func(e uint32, _ int) int { return n.compare(int(e), old, new) })
	if !ok {
		return Lines{}
	}
	return n.lines[n.order[k]]
}

// ForRaw returns the counts of raw's entries, in its order: a list and
// its counts are read apart, the list first, as it is quicker.
func (n *NumStat) ForRaw(raw *Raw) []Lines {
	if n == nil || raw == nil {
		return nil
	}
	out := make([]Lines, raw.Len())
	for i := range out {
		out[i] = n.Find(raw.OldPath(i), raw.Path(i))
	}
	return out
}

// ParseNumStat reads the output of git diff --numstat -z: per file the
// added and deleted counts, "-" for a binary file, then its path, or for
// a rename or copy an empty path and the old and new ones.
func ParseNumStat(data []byte) (*NumStat, error) {
	n := &NumStat{}
	next := func() ([]byte, bool) {
		i := bytes.IndexByte(data, 0)
		if i < 0 {
			return nil, false
		}
		f := data[:i]
		data = data[i+1:]
		return f, true
	}
	for len(data) > 0 {
		head, ok := next()
		if !ok {
			return nil, ErrMalformed
		}
		added, rest, ok1 := bytes.Cut(head, []byte{'\t'})
		deleted, path, ok2 := bytes.Cut(rest, []byte{'\t'})
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("%w: %q", ErrMalformed, head)
		}
		l := Lines{Known: true}
		if string(added) == "-" && string(deleted) == "-" {
			l.Binary = true
		} else {
			a, err1 := strconv.ParseUint(string(added), 10, 32)
			d, err2 := strconv.ParseUint(string(deleted), 10, 32)
			if err1 != nil || err2 != nil {
				return nil, fmt.Errorf("%w: %q", ErrMalformed, head)
			}
			l.Added, l.Deleted = uint32(a), uint32(d)
		}
		old, new := path, path
		if len(path) == 0 {
			if old, ok = next(); !ok {
				return nil, ErrMalformed
			}
			if new, ok = next(); !ok {
				return nil, ErrMalformed
			}
		}
		n.paths = append(n.paths, old...)
		n.ends = append(n.ends, uint32(len(n.paths)))
		n.paths = append(n.paths, new...)
		n.ends = append(n.ends, uint32(len(n.paths)))
		n.lines = append(n.lines, l)
	}
	n.order = make([]uint32, len(n.lines))
	for i := range n.order {
		n.order[i] = uint32(i)
	}
	slices.SortFunc(n.order, func(a, b uint32) int {
		return n.compare(int(a), n.path(2*int(b)), n.path(2*int(b)+1))
	})
	return n, nil
}

// numStat runs a diff that prints --numstat -z, its paths as literal.
func numStat(ctx context.Context, r *run.Runner, gitDir, workTree string, args []string) (*NumStat, error) {
	c := numStatCmd(gitDir, workTree, args)
	res, err := r.Run(ctx, &c)
	if err != nil {
		return nil, err
	}
	return ParseNumStat(res.Stdout)
}

func numStatCmd(gitDir, workTree string, args []string) run.Cmd {
	return run.Cmd{GitDir: gitDir, WorkTree: workTree, Args: args, Read: true, Untranslated: true,
		Env: []string{"GIT_LITERAL_PATHSPECS=1"}}
}

// CommitLines counts the lines changed from commit or tree a to b, with
// renames, through git diff, so that diff.algorithm counts them as the
// patch CommitPatch reads shows them.
func CommitLines(ctx context.Context, r *run.Runner, gitDir string, a, b []byte) (*NumStat, error) {
	args := []string{"diff", "--numstat", "-z", "--no-color", "--no-ext-diff", "--submodule=short", "-M",
		hex.EncodeToString(a), hex.EncodeToString(b)}
	return numStat(ctx, r, gitDir, "", args)
}

// WorkTreeBaseLines counts the lines changed from commit or tree a to
// the work tree, with renames, as WorkTreeRenames lists them, through
// the plumbing, which never writes the index.
func WorkTreeBaseLines(ctx context.Context, r *run.Runner, gitDir, workTree string, a []byte) (*NumStat, error) {
	args := []string{"diff-index", "--numstat", "-z", "--submodule=short", "-M", hex.EncodeToString(a)}
	return numStat(ctx, r, gitDir, workTree, args)
}

// WorkTreeLines counts the lines changed in the work tree, against the
// index, or, staged, in the index against the tree-ish base, renames
// found when renames is set. paths limit it, matched literally; none, or
// more than a command line takes on every platform (run.MaxArgBytes),
// count every change: one command, so that renames pair across all of
// them. Like the patches of a worktree another program may work in, it
// runs the plumbing, which never writes the index.
func WorkTreeLines(ctx context.Context, r *run.Runner, gitDir, workTree string, staged bool, base string, renames bool, paths []string) (*NumStat, error) {
	args := []string{"diff-files", "--numstat", "-z", "--submodule=short"}
	if staged {
		args = []string{"diff-index", "--cached", "--numstat", "-z", "--submodule=short"}
		if renames {
			args = append(args, "-M")
		}
		args = append(args, base)
	}
	if len(paths) > 0 {
		named := append(append(slices.Clip(args), "--"), paths...)
		if c := numStatCmd(gitDir, workTree, named); c.LineBytes() <= run.MaxArgBytes {
			args = named
		}
	}
	return numStat(ctx, r, gitDir, workTree, args)
}

// firstFew is how much of a file git reads to tell whether it is
// binary.
const firstFew = 8000

// CountText counts the lines of b as git diff counts a file added whole:
// a last line without its line end counts, and a NUL in the first 8000
// bytes makes it binary.
func CountText(b []byte) Lines {
	if bytes.IndexByte(b[:min(len(b), firstFew)], 0) >= 0 {
		return Lines{Binary: true, Known: true}
	}
	n := bytes.Count(b, []byte{'\n'})
	if len(b) > 0 && b[len(b)-1] != '\n' {
		n++
	}
	return Lines{Added: uint32(n), Known: true}
}
