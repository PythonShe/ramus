package diff

import (
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
)

// DefaultMaxPatch bounds a patch held for display.
const DefaultMaxPatch = 256 << 20

// PatchRequest asks for the patch of some paths.
type PatchRequest struct {
	Source Source
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
	if req.Context >= 0 {
		args = append(args, "-U"+strconv.Itoa(req.Context))
	}
	var config []string
	if req.ForApply {
		args = append(args, "--no-textconv", "--src-prefix=a/", "--dst-prefix=b/", "--no-relative", "--no-color-moved")
		config = []string{"diff.suppressBlankEmpty=false"}
	}
	switch req.Source {
	case Staged:
		args = append(args, "--cached")
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
	return Patch{Text: w.buf}, nil
}

// capped keeps up to max bytes and then stops git.
type capped struct {
	buf    []byte
	max    int
	full   bool
	cancel func()
}

var errFull = errors.New("diff: patch over its limit")

func (c *capped) Write(b []byte) (int, error) {
	if c.full {
		return 0, errFull
	}
	if len(c.buf)+len(b) > c.max {
		c.buf = append(c.buf, b[:c.max-len(c.buf)]...)
		c.full = true
		c.cancel()
		return 0, errFull
	}
	c.buf = append(c.buf, b...)
	return len(b), nil
}

// cut drops a partial last line.
func (c *capped) cut() []byte {
	for i := len(c.buf) - 1; i >= 0; i-- {
		if c.buf[i] == '\n' {
			return c.buf[:i+1]
		}
	}
	return nil
}
