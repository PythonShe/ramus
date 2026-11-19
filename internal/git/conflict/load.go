package conflict

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"ramus/internal/git/run"
)

// Load splits data, the work tree's text of path, a file git left
// conflicted, into segments: it reads markers of the size git wrote them
// in, and tells text that only looks like a conflict from git's by merging
// the sides in the index again (see remerge).
//
// git writes markers of the size the file's conflict-marker-size attribute
// had when it merged, in the work tree's attributes then: HEAD's, unless
// they were edited and not committed. The merge may change them since, as
// when the merged branch sets the attribute. So the file is read at HEAD's
// size, then at the work tree's, then at the size of its own lines that
// pair up as markers do (see other). A file the index holds conflicted is
// read as having no conflicts only when every line in it like one of git's
// markers can be text of its sides.
//
// A file that takes past loadTime to read is refused as one whose
// conflicts cannot be told apart.
func Load(ctx context.Context, r *run.Runner, gitDir, workTree, path string, data []byte) ([]Segment, error) {
	if len(data) > MaxSize {
		return nil, errLarge
	}
	in, cancel := context.WithTimeout(ctx, loadTime)
	defer cancel()
	segs, err := load(in, r, gitDir, workTree, path, data)
	if err != nil && in.Err() != nil && ctx.Err() == nil {
		return nil, ErrAmbiguous
	}
	return segs, err
}

// loadTime bounds the reading of one file. Merging the sides again takes
// most of it: git merge-file's histogram diff, which ort merges with,
// takes seconds on a file of tens of megabytes.
var loadTime = 20 * time.Second

func load(ctx context.Context, r *run.Runner, gitDir, workTree, path string, data []byte) ([]Segment, error) {
	sizes, err := markerSizes(ctx, r, gitDir, workTree, path)
	if err != nil {
		return nil, err
	}
	l := &loader{ctx: ctx, r: r, gitDir: gitDir, workTree: workTree, path: path, data: data}
	var none []Segment
	var refused error
	for i, size := range sizes {
		segs, err := l.at(size)
		switch {
		case err == nil && Count(segs) > 0:
			return l.finish(segs)
		case err == nil:
			if i == 0 {
				none = segs
			}
		case errors.Is(err, ErrAmbiguous) || errors.Is(err, ErrMarkers):
			refused = cmp.Or(refused, err)
		default:
			return nil, err
		}
	}
	return l.other(none, refused, sizes)
}

// loader reads one file, the sides in the index once, when needed.
type loader struct {
	ctx                    context.Context
	r                      *run.Runner
	gitDir, workTree, path string
	data                   []byte
	sides                  *mergedSides
}

func (l *loader) readSides() (mergedSides, error) {
	if l.sides == nil {
		s, err := readSides(l.ctx, l.r, l.gitDir, l.workTree, l.path, len(l.data))
		if err != nil {
			return s, err
		}
		l.sides = &s
	}
	return *l.sides, nil
}

// finish takes off the sides of a conflict that ends the file the line
// ending git added to them (see trimAdded).
func (l *loader) finish(segs []Segment) ([]Segment, error) {
	if len(segs) == 0 || segs[len(segs)-1].Conflict == nil {
		return segs, nil
	}
	s, err := l.readSides()
	if err != nil {
		return nil, err
	}
	trimAdded(segs, endingOf(s.Ours), endingOf(s.Base), endingOf(s.Theirs))
	return segs, nil
}

// at reads the file at markers of size, one its attributes give.
func (l *loader) at(size int) ([]Segment, error) {
	if !hasOpen(l.data, size) {
		return parseSize(l.data, size)
	}
	s, err := l.readSides()
	if err != nil {
		return nil, err
	}
	if s.holdsNested(size) {
		return nil, ErrAmbiguous
	}
	if !s.holdsMarkers(size) {
		// Every marker in the file is git's, so each is one of a
		// conflict read: one left as text, as markers after an open
		// marker git wrote on a line, is a conflict missed.
		segs, err := parseSize(l.data, size)
		if err == nil && strayMarkers(l.data, size, segs) {
			return nil, ErrAmbiguous
		}
		return segs, err
	}
	return remerge(l.ctx, l.r, l.data, size, s)
}

// strayMarkers reports whether data holds more lines like markers of size
// than the conflicts of segs, its reading at size, are made of.
func strayMarkers(data []byte, size int, segs []Segment) bool {
	marks := 0
	for _, sg := range segs {
		if c := sg.Conflict; c != nil {
			marks += 3
			if c.HasBase {
				marks++
			}
		}
	}
	for pos := 0; pos < len(data); {
		line, next := lineAt(data, pos)
		if kind, _ := markerOf(line, size); kind != none {
			if marks--; marks < 0 {
				return true
			}
		}
		pos = next
	}
	return false
}

// other reads the file at the size of lines in it that pair up as markers
// of a size the attributes did not give (a separator, then a close
// marker), when a merge of the sides there is the file; else the reading
// at the attributes' sizes stands, segs or the refusal, but a reading with
// no conflict only when every line like a marker of those sizes can be
// text of the sides. git writes the separator and the close marker of each
// conflict on lines of their own, the open marker after a side's last
// line, which has none when ignoring white space matched it with a line
// that has one.
func (l *loader) other(segs []Segment, refused error, sizes []int) ([]Segment, error) {
	paired := pairedSizes(l.data)
	if len(paired) == 0 {
		return segs, refused
	}
	s, err := l.readSides()
	if err != nil {
		return nil, err
	}
	if s.Base == nil && s.Ours == nil && s.Theirs == nil {
		return segs, refused // not conflicted in the index
	}
	text := map[int]bool{}
	for _, n := range paired {
		if slices.Contains(sizes, n) || s.Ours == nil || s.Theirs == nil {
			continue
		}
		if at, err := parseSize(l.data, n); err != nil || Count(at) == 0 {
			continue
		}
		at, err := remerge(l.ctx, l.r, l.data, n, s)
		switch {
		case err == nil && Count(at) > 0:
			return l.finish(at)
		case err == nil:
			text[n] = true // the file is a merge with no conflict
		case !errors.Is(err, ErrAmbiguous):
			return nil, err
		}
	}
	if refused != nil {
		return nil, refused
	}
	for _, n := range paired {
		if !text[n] && !s.hold(l.data, n) {
			return nil, ErrAmbiguous
		}
	}
	return segs, nil
}

// pairedSizes are the sizes of the separator lines in data that a close
// marker of the same size follows, smallest first.
func pairedSizes(data []byte) []int {
	seps := map[int]bool{}
	var paired []int
	for pos := 0; pos < len(data); {
		line, next := lineAt(data, pos)
		pos = next
		n := runOf(line)
		if n == 0 {
			continue
		}
		switch kind, _ := markerOf(line, n); kind {
		case sep:
			seps[n] = true
		case closeMark:
			if seps[n] && !slices.Contains(paired, n) {
				paired = append(paired, n)
			}
		}
	}
	slices.Sort(paired)
	return paired
}

// runOf is the length of the run of one of the characters markers are made
// of that line starts with.
func runOf(line []byte) int {
	if len(line) == 0 || !strings.ContainsRune("<|=>", rune(line[0])) {
		return 0
	}
	n := 1
	for n < len(line) && line[n] == line[0] {
		n++
	}
	return n
}

// markerSizes are the sizes git may have written path's markers in, as
// its conflict-marker-size attribute sets them: in HEAD, and in the work
// tree. HEAD's comes first.
func markerSizes(ctx context.Context, r *run.Runner, gitDir, workTree, path string) ([]int, error) {
	now, err := markerSize(ctx, r, gitDir, workTree, path)
	if err != nil {
		return nil, err
	}
	head, err := markerSize(ctx, r, gitDir, workTree, path, "--source=HEAD")
	if err != nil {
		if ctx.Err() != nil {
			return nil, err
		}
		// No commit yet, or a git without check-attr --source (2.40).
		return []int{now}, nil
	}
	if head == now {
		return []int{now}, nil
	}
	return []int{head, now}, nil
}

// markerSize is the size of the conflict markers git writes for path, as
// its conflict-marker-size attribute sets it, read with extra options.
func markerSize(ctx context.Context, r *run.Runner, gitDir, workTree, path string, extra ...string) (int, error) {
	args := append(append([]string{"check-attr", "-z"}, extra...), "conflict-marker-size", "--", path)
	res, err := r.Run(ctx, &run.Cmd{GitDir: gitDir, WorkTree: workTree, Read: true, Args: args})
	if err != nil {
		return 0, err
	}
	// path NUL attribute NUL value NUL
	f := bytes.Split(res.Stdout, []byte{0})
	if len(f) < 3 {
		return 0, fmt.Errorf("conflict: check-attr printed %q", res.Stdout)
	}
	return markerSizeOf(string(f[2])), nil
}

// markerSizeOf is the marker size git merges with for an attribute value,
// as ll_merge reads it: a number that fits in a C int (strtol_i, which
// skips leading white space and takes a sign) and is positive, else the
// default. ("set", "unset" and "unspecified" are no number.)
func markerSizeOf(v string) int {
	n, err := strconv.ParseInt(strings.TrimLeft(v, " \t\n\v\f\r"), 10, 32)
	if err != nil || n <= 0 {
		return defaultMarkerSize
	}
	return int(n)
}

func hasOpen(data []byte, size int) bool {
	for pos := 0; pos < len(data); {
		line, next := lineAt(data, pos)
		if kind, _ := markerOf(line, size); kind == open {
			return true
		}
		pos = next
	}
	return false
}

// readSides reads the stages of path in the index, each about hint bytes.
func readSides(ctx context.Context, r *run.Runner, gitDir, workTree, path string, hint int) (mergedSides, error) {
	var s mergedSides
	res, err := r.Run(ctx, &run.Cmd{GitDir: gitDir, WorkTree: workTree, Read: true,
		Env:  []string{"GIT_LITERAL_PATHSPECS=1"},
		Args: []string{"ls-files", "-u", "-z", "--", path}})
	if err != nil {
		return s, err
	}
	for rec := range bytes.SplitSeq(res.Stdout, []byte{0}) {
		// mode SP id SP stage TAB path
		meta, name, ok := bytes.Cut(rec, []byte{'\t'})
		f := bytes.Fields(meta)
		if !ok || string(name) != path || len(f) != 3 {
			continue
		}
		var side *[]byte
		switch string(f[2]) {
		case "1":
			side = &s.Base
		case "2":
			side = &s.Ours
		case "3":
			side = &s.Theirs
		default:
			continue
		}
		// Grown once to about the size a side has: as large as a merge
		// of it, the file.
		var blob bytes.Buffer
		blob.Grow(min(hint+hint/8+512, MaxSize))
		w := &limited{b: &blob, max: MaxSize}
		_, err := r.Run(ctx, &run.Cmd{GitDir: gitDir, Read: true, Stdout: w,
			Args: []string{"cat-file", "blob", string(f[1])}})
		if w.over {
			return s, errLarge
		}
		if err != nil {
			return s, err
		}
		*side = blob.Bytes()
	}
	return s, nil
}
