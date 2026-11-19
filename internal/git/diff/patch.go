package diff

import (
	"bytes"
	"context"
	"errors"
	"strconv"

	"ramus/internal/git/run"
)

// What a work-tree patch compares.
type Source int

const (
	// Unstaged compares the work tree with the index.
	Unstaged Source = iota
	// Staged compares the index with HEAD.
	Staged
	// Untracked shows a file git does not track as added whole.
	Untracked
	// Head compares the work tree with HEAD, staged or not: all that
	// changed there since the last commit. It runs the plumbing
	// diff-index, which never writes the index: git diff refreshes it,
	// --no-optional-locks or not, which in a worktree another program
	// works in could take the lock from under it.
	Head
)

// Whitespace is what changes of white space a patch leaves out.
type Whitespace uint8

const (
	// AllWhitespace shows every change.
	AllWhitespace Whitespace = iota
	// IgnoreSpaceChange leaves out changes in the amount of white space,
	// as git diff -b.
	IgnoreSpaceChange
	// IgnoreAllSpace compares lines without their white space, as git
	// diff -w.
	IgnoreAllSpace
)

func (w Whitespace) args() []string {
	switch w {
	case IgnoreSpaceChange:
		return []string{"--ignore-space-change"}
	case IgnoreAllSpace:
		return []string{"--ignore-all-space"}
	}
	return nil
}

// DefaultMaxPatch bounds a patch held for display.
const DefaultMaxPatch = 256 << 20

// PatchRequest asks for the patch of some paths.
type PatchRequest struct {
	Source Source
	// Base is the commit or tree Head compares the work tree with, as
	// hex; "" is HEAD.
	Base string
	// Paths limits the patch, matched literally; empty is every path,
	// except for Untracked, which takes exactly one.
	Paths []string
	// Context is the number of context lines; negative keeps git's
	// diff.context.
	Context int
	// MaxBytes cuts the patch off; DefaultMaxPatch when zero.
	MaxBytes int
	// ForApply asks for a patch that git apply takes back, for staging
	// and discarding parts of it: no textconv, standard a/ and b/
	// prefixes and blank context lines kept, whatever the user's config.
	ForApply bool
	// Whitespace leaves changes of white space out; such a patch is for
	// reading, and git apply may not take it back.
	Whitespace Whitespace
	// Renames finds renames among Paths whatever diff.renames says: a
	// file status found renamed shows against its old path.
	Renames bool
	// NoRefresh compares the work tree with the index through the
	// plumbing diff-files, which never writes the index, where git diff
	// refreshes it: for a worktree another program works in, whose own
	// git would find the index locked. It leaves out the diff settings
	// only the porcelain reads, as diff.algorithm.
	NoRefresh bool
}

// Patch is a unified diff as git prints it.
type Patch struct {
	Text []byte
	// Truncated is set when the patch was cut off at MaxBytes, at a line
	// end; the rest was not read.
	Truncated bool
}

// WorkTreePatch streams a patch of the work tree or the index from git
// diff, so that clean and smudge filters, line endings, textconv and
// diff.algorithm apply as in the terminal. External diff drivers do not
// run: the app shows the patch itself.
func WorkTreePatch(ctx context.Context, r *run.Runner, gitDir, workTree string, req PatchRequest) (Patch, error) {
	args := []string{"diff", "--no-color", "--no-ext-diff", "--submodule=short"}
	switch {
	case req.Source == Head:
		args = []string{"diff-index", "-p", "--no-color", "--no-ext-diff", "--submodule=short"}
	case req.Source == Unstaged && req.NoRefresh:
		args = []string{"diff-files", "-p", "--no-color", "--no-ext-diff", "--submodule=short"}
	}
	if req.Context >= 0 {
		args = append(args, "-U"+strconv.Itoa(req.Context))
	}
	var config []string
	args = append(args, req.Whitespace.args()...)
	if req.ForApply {
		args = append(args, "--no-textconv", "--src-prefix=a/", "--dst-prefix=b/", "--no-relative", "--no-color-moved")
		config = []string{"diff.suppressBlankEmpty=false"}
	}
	if req.Renames {
		args = append(args, "--find-renames")
	}
	switch req.Source {
	case Staged:
		args = append(args, "--cached")
	case Head:
		base := "HEAD"
		if req.Base != "" {
			base = req.Base
		}
		args = append(args, "--end-of-options", base)
	case Untracked:
		if len(req.Paths) != 1 {
			return Patch{}, errors.New("diff: an untracked patch takes one path")
		}
		args = append(args, "--no-index", "--", "/dev/null", req.Paths[0])
	}
	if req.Source != Untracked {
		args = append(append(args, "--"), req.Paths...)
	}
	// --no-index exits 1 when the files differ.
	return runPatch(ctx, r, gitDir, workTree, args, config, req.MaxBytes, req.Source == Untracked)
}

func runPatch(ctx context.Context, r *run.Runner, gitDir, workTree string, args, config []string, max int, exit1 bool) (Patch, error) {
	if max <= 0 {
		max = DefaultMaxPatch
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w := &capped{max: max, cancel: cancel}
	_, err := r.Run(ctx, &run.Cmd{
		GitDir: gitDir, WorkTree: workTree, Args: args, Read: true, Stdout: w,
		// Paths are names, not patterns; quotePath off keeps them as they
		// are in the headers, as the patch parser expects.
		Config: append([]string{"core.quotePath=false"}, config...),
		Env:    []string{"GIT_LITERAL_PATHSPECS=1"},
	})
	if w.full {
		return Patch{Text: w.cut(), Truncated: true}, nil
	}
	var e *run.Error
	if err != nil && exit1 && errors.As(err, &e) && e.ExitCode == 1 {
		err = nil
	}
	if err != nil {
		return Patch{}, err
	}
	return Patch{Text: w.text()}, nil
}

// capped keeps up to max bytes and then stops git.
type capped struct {
	// parts are the writes as they came, joined once at the end: one
	// buffer grown by append would copy a large patch several times over.
	parts  [][]byte
	n      int
	max    int
	full   bool
	cancel func()
}

var errFull = errors.New("diff: patch over its limit")

func (c *capped) Write(b []byte) (int, error) {
	if c.full {
		return 0, errFull
	}
	if c.n+len(b) > c.max {
		c.add(b[:c.max-c.n])
		c.full = true
		c.cancel()
		return 0, errFull
	}
	c.add(b)
	return len(b), nil
}

func (c *capped) add(b []byte) {
	if len(b) > 0 {
		c.parts = append(c.parts, bytes.Clone(b))
		c.n += len(b)
	}
}

// text is all that was kept, nil for nothing.
func (c *capped) text() []byte {
	if c.n == 0 {
		return nil
	}
	return bytes.Join(c.parts, nil)
}

// cut is what was kept without a partial last line.
func (c *capped) cut() []byte {
	b := c.text()
	if i := bytes.LastIndexByte(b, '\n'); i >= 0 {
		return b[:i+1]
	}
	return nil
}
