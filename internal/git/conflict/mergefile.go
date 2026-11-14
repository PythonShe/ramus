package conflict

import (
	"context"
	"errors"
	"os"
	"strings"

	"ramus/internal/git/run"
)

// Files is the merge of three files that git mergetool hands over.
type Files struct {
	// Segments are the merge split at its conflicts; nil when Binary.
	Segments []Segment
	Binary   bool
}

// ErrTooLarge is a merge larger than MaxSize.
var ErrTooLarge = errors.New("conflict: the merge is too large to resolve here")

// MergeFiles merges the change from base to remote into local, absolute
// paths, with git merge-file outside any repository; base "" is none.
// Each side is labelled with its name in labels: local, base, remote.
func MergeFiles(ctx context.Context, r *run.Runner, base, local, remote string, labels [3]string) (Files, error) {
	if base == "" {
		// An empty file: git for Windows cannot stat /dev/null here.
		f, err := os.CreateTemp("", "ramus-base-*")
		if err != nil {
			return Files{}, err
		}
		f.Close()
		defer os.Remove(f.Name())
		base = f.Name()
	}
	var out strings.Builder
	w := &limited{b: &out, max: MaxSize}
	_, err := r.Run(ctx, &run.Cmd{
		Outside: true, WorkTree: "/", Read: true, Untranslated: true, Stdout: w,
		Args: []string{"merge-file", "-p", "--diff3", "-L", labels[0], "-L", labels[1], "-L", labels[2], "--", local, base, remote},
	})
	if w.over {
		return Files{}, ErrTooLarge
	}
	var e *run.Error
	switch {
	case err == nil:
	case errors.As(err, &e) && e.ExitCode > 0 && e.ExitCode < 128:
		// The number of conflicts.
	case errors.As(err, &e) && strings.Contains(e.Stderr, "Cannot merge binary files"):
		return Files{Binary: true}, nil
	default:
		return Files{}, err
	}
	segs, err := Parse([]byte(out.String()))
	if err != nil {
		return Files{}, err
	}
	return Files{Segments: segs}, nil
}

// limited keeps up to max bytes, then refuses the rest, which stops git.
type limited struct {
	b    *strings.Builder
	max  int
	over bool
}

func (l *limited) Write(p []byte) (int, error) {
	if l.b.Len()+len(p) > l.max {
		l.over = true
		return 0, ErrTooLarge
	}
	return l.b.Write(p)
}
