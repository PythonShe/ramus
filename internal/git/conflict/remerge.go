package conflict

import (
	"bytes"
	"context"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"

	"ramus/internal/git/run"
)

// remergeVariants are the ways git may have merged the file: each
// conflict style, and the diff algorithms its merges use (myers, which
// git merge-file and the recursive strategy use, and histogram, which
// ort does). A git without --diff-algorithm for merge-file skips those.
var remergeVariants = [][]string{
	nil, {"--diff3"}, {"--zdiff3"},
	{"--diff-algorithm=histogram"}, {"--diff3", "--diff-algorithm=histogram"}, {"--zdiff3", "--diff-algorithm=histogram"},
}

// remerge reads data, a file git left conflicted whose sides hold lines
// like conflict markers of size, by merging the sides again with markers
// longer than any line in them, which nothing can be mistaken for: the
// conflicts are where that merge puts them, and data must be that merge
// with git's markers of size, any labels on them, line for line. A merge
// data can be read as in two ways, or as none, as when a conflict was
// edited by hand, is ErrAmbiguous: a guess could drop a line, once
// resolved, that was in no conflict.
func remerge(ctx context.Context, r *run.Runner, data []byte, size int, s mergedSides) ([]Segment, error) {
	if s.Ours == nil || s.Theirs == nil {
		// One side deleted: git leaves the other as it is, all text.
		side := s.Ours
		if side == nil {
			side = s.Theirs
		}
		if side != nil && (bytes.Equal(data, side) || bytes.Equal(data, crlf(side))) {
			return []Segment{{Text: data}}, nil
		}
		return nil, ErrAmbiguous
	}
	dir, err := os.MkdirTemp("", "ramus-remerge-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	var paths [3]string
	long := size
	for i, b := range [][]byte{s.Ours, s.Base, s.Theirs} {
		paths[i] = dir + string(os.PathSeparator) + strconv.Itoa(i)
		if err := os.WriteFile(paths[i], b, 0o600); err != nil {
			return nil, err
		}
		n, _, err := longestRun(paths[i])
		if err != nil {
			return nil, err
		}
		long = max(long, n)
	}
	long++
	var found []Segment
	matched := false
	for _, v := range remergeVariants {
		out, err := runMergeFile(ctx, r, paths, long, v...)
		var e *run.Error
		switch {
		case err == nil:
		case errors.As(err, &e) && e.ExitCode == 129:
			continue // an option this git does not have
		case errors.Is(err, errBinary):
			return nil, ErrAmbiguous
		default:
			return nil, err
		}
		segs, err := parseSize([]byte(out), long)
		if err != nil {
			return nil, err
		}
		for _, conv := range []bool{false, true} {
			got, ok := match(data, size, segs, conv)
			if !ok {
				continue
			}
			if matched && !sameReading(found, got) {
				return nil, ErrAmbiguous
			}
			found, matched = got, true
		}
	}
	if !matched {
		return nil, ErrAmbiguous
	}
	return found, nil
}

// match reads data as segs, a merge's segments, written with markers of
// size and any labels; conv reads data in CRLF where segs are in LF.
func match(data []byte, size int, segs []Segment, conv bool) ([]Segment, bool) {
	pos := 0
	take := func(want []byte) ([]byte, bool) {
		if conv {
			want = crlf(want)
		}
		if !bytes.HasPrefix(data[pos:], want) {
			return nil, false
		}
		got := data[pos : pos+len(want) : pos+len(want)]
		pos += len(want)
		return got, true
	}
	mark := func(kind marker) (string, bool) {
		if pos >= len(data) {
			return "", false
		}
		line, next := lineAt(data, pos)
		k, n := markerOf(line, size)
		if k != kind || conv && !bytes.HasSuffix(line, []byte("\r\n")) {
			return "", false
		}
		pos = next
		return label(line, n), true
	}
	var out []Segment
	for _, sg := range segs {
		var ok bool
		if sg.Conflict == nil {
			var t []byte
			if t, ok = take(sg.Text); !ok {
				return nil, false
			}
			out = append(out, Segment{Text: t})
			continue
		}
		c, g := sg.Conflict, &Conflict{HasBase: sg.Conflict.HasBase}
		if g.OursLabel, ok = mark(open); !ok {
			return nil, false
		}
		if g.Ours, ok = take(c.Ours); !ok {
			return nil, false
		}
		if c.HasBase {
			if g.BaseLabel, ok = mark(baseMark); !ok {
				return nil, false
			}
			if g.Base, ok = take(c.Base); !ok {
				return nil, false
			}
		}
		if _, ok = mark(sep); !ok {
			return nil, false
		}
		if g.Theirs, ok = take(c.Theirs); !ok {
			return nil, false
		}
		if g.TheirsLabel, ok = mark(closeMark); !ok {
			return nil, false
		}
		out = append(out, Segment{Conflict: g})
	}
	return out, pos == len(data)
}

// sameReading reports whether two readings split a file alike.
func sameReading(a, b []Segment) bool {
	return slices.EqualFunc(a, b, func(x, y Segment) bool {
		if (x.Conflict == nil) != (y.Conflict == nil) {
			return false
		}
		if x.Conflict == nil {
			return bytes.Equal(x.Text, y.Text)
		}
		p, q := x.Conflict, y.Conflict
		return p.HasBase == q.HasBase && bytes.Equal(p.Ours, q.Ours) && bytes.Equal(p.Base, q.Base) && bytes.Equal(p.Theirs, q.Theirs)
	})
}

// crlf is b with each line ended in CRLF, as a checkout with eol=crlf
// writes it.
func crlf(b []byte) []byte {
	if !bytes.Contains(b, []byte{'\n'}) {
		return b
	}
	var out []byte
	for pos := 0; pos < len(b); {
		line, next := lineAt(b, pos)
		if bytes.HasSuffix(line, []byte{'\n'}) && !bytes.HasSuffix(line, []byte("\r\n")) {
			line = append(line[:len(line)-1:len(line)-1], '\r', '\n')
		}
		out = append(out, line...)
		pos = next
	}
	return out
}

var errBinary = errors.New("conflict: binary")

// runMergeFile runs git merge-file -p on paths (ours, base, theirs) with
// markers of size and extra options, outside any repository, and returns
// what it writes; conflicts are no failure.
func runMergeFile(ctx context.Context, r *run.Runner, paths [3]string, size int, extra ...string) (string, error) {
	var out strings.Builder
	w := &limited{b: &out, max: MaxSize}
	args := append([]string{"merge-file", "-p", "--marker-size=" + strconv.Itoa(size)}, extra...)
	_, err := r.Run(ctx, &run.Cmd{
		Outside: true, WorkTree: "/", Read: true, Untranslated: true, Stdout: w,
		Args: append(args, "--", paths[0], paths[1], paths[2]),
	})
	if w.over {
		return "", ErrTooLarge
	}
	var e *run.Error
	switch {
	case err == nil:
	case errors.As(err, &e) && e.ExitCode > 0 && e.ExitCode < 128:
		// The number of conflicts.
	case errors.As(err, &e) && strings.Contains(e.Stderr, "Cannot merge binary files"):
		return "", errBinary
	default:
		return "", err
	}
	return out.String(), nil
}
