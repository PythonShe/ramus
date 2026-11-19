package conflict

import (
	"context"
	"errors"
	"io"
	"os"
	"strconv"
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
	// Markers longer than any run of marker characters in the files, so
	// that no text in them is read as one.
	size := DefaultMarkerSize
	for _, p := range []string{local, base, remote} {
		n, err := longestRun(p)
		if err != nil {
			return Files{}, err
		}
		size = max(size, n+1)
	}
	var out strings.Builder
	w := &limited{b: &out, max: MaxSize}
	_, err := r.Run(ctx, &run.Cmd{
		Outside: true, WorkTree: "/", Read: true, Untranslated: true, Stdout: w,
		Args: []string{"merge-file", "-p", "--diff3", "--marker-size=" + strconv.Itoa(size),
			"-L", labels[0], "-L", labels[1], "-L", labels[2], "--", local, base, remote},
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
	segs, err := ParseSize([]byte(out.String()), size)
	if err != nil {
		return Files{}, err
	}
	return Files{Segments: segs}, nil
}

// longestRun is the longest run of one of the characters conflict markers
// are made of at the start of a line of the file at path.
func longestRun(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	buf := make([]byte, 64<<10)
	longest, n := 0, 0
	var c byte = '\n' // the run's character; a newline before the first line
	for {
		k, err := f.Read(buf)
		for _, b := range buf[:k] {
			switch {
			case b == '\n':
				c, n = '\n', 0
			case c == '\n' && (b == '<' || b == '=' || b == '|' || b == '>'):
				c, n = b, 1
			case c != 0 && b == c:
				n++
			default:
				c, n = 0, 0 // the rest of the line
			}
			longest = max(longest, n)
		}
		if err == io.EOF {
			return longest, nil
		}
		if err != nil {
			return 0, err
		}
	}
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
