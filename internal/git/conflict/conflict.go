// Package conflict reads the conflict markers git leaves in a file that
// did not merge cleanly, and writes the file again with each conflict
// resolved as chosen.
package conflict

import (
	"bytes"
	"errors"
	"fmt"
)

// MaxSize bounds the files read for conflicts.
const MaxSize = 32 << 20

// Segment is a run of the file: text both sides agree on, or a conflict.
type Segment struct {
	Text     []byte
	Conflict *Conflict
}

// Conflict is a region the sides changed differently. Each side holds its
// lines with their line endings.
type Conflict struct {
	Ours, Base, Theirs []byte
	// HasBase is set when git wrote the base too (diff3 and zdiff3).
	HasBase bool
	// OursLabel and TheirsLabel are what git wrote after the markers,
	// as "HEAD" and the branch merged.
	OursLabel, BaseLabel, TheirsLabel string
}

// Choice is how a conflict is resolved.
type Choice uint8

const (
	Unresolved Choice = iota
	Ours
	Theirs
	// OursThenTheirs keeps both, ours first; TheirsThenOurs the other way.
	OursThenTheirs
	TheirsThenOurs
	Base
	// Custom is text the user wrote.
	Custom
)

// ErrMarkers is a file whose markers do not pair up.
var ErrMarkers = errors.New("conflict: the conflict markers do not pair up")

type marker uint8

const (
	none marker = iota
	open
	baseMark
	sep
	closeMark
)

// defaultMarkerSize is the size of the markers git writes for a file with
// no conflict-marker-size attribute.
const defaultMarkerSize = 7

// markerOf reports whether line is a conflict marker of size characters,
// and of what kind: size of one character, then a space or the line's
// end. git writes markers of one size for a file; longer ones are of a
// conflict nested in a side, shorter ones text.
func markerOf(line []byte, size int) (marker, int) {
	if len(line) < size {
		return none, 0
	}
	c := line[0]
	var kind marker
	switch c {
	case '<':
		kind = open
	case '|':
		kind = baseMark
	case '=':
		kind = sep
	case '>':
		kind = closeMark
	default:
		return none, 0
	}
	n := 0
	for n < len(line) && line[n] == c {
		n++
	}
	if n != size {
		return none, 0
	}
	rest := line[n:]
	switch {
	case len(rest) == 0, rest[0] == '\n', rest[0] == '\r':
	case rest[0] == ' ' && kind != sep:
	default:
		return none, 0
	}
	return kind, n
}

func label(line []byte, n int) string {
	return string(bytes.TrimSpace(line[n:]))
}

// Parse is parseSize with git's default marker size.
func Parse(data []byte) ([]Segment, error) {
	return parseSize(data, defaultMarkerSize)
}

// parseSize splits data into segments at conflict markers of size
// characters, the size git wrote them in (see Load). A marker out of place
// is read as text of the side it is in; parseSize fails when a conflict is
// not closed, or the file is larger than MaxSize.
//
// Text in the file that only looks like a conflict, as an example in a
// document, is read as one: Load tells them apart.
func parseSize(data []byte, size int) ([]Segment, error) {
	if len(data) > MaxSize {
		return nil, errLarge
	}
	var p parsed
	for pos := 0; pos < len(data); {
		line, next := lineAt(data, pos)
		if kind, _ := markerOf(line, size); kind != open {
			// A line like a marker outside a conflict, as a
			// reStructuredText underline, is text.
			p.text = append(p.text, line...)
			pos = next
			continue
		}
		c, end, _ := conflictAt(data, pos, size)
		if c == nil {
			return nil, ErrMarkers
		}
		p.add(c)
		pos = end
	}
	return p.done(), nil
}

var errLarge = fmt.Errorf("conflict: the file is larger than %d MB", MaxSize>>20)

// parsed gathers segments: text up to a conflict, the conflict, and so on.
type parsed struct {
	segs []Segment
	text []byte
}

func (p *parsed) add(c *Conflict) {
	if len(p.text) > 0 {
		p.segs = append(p.segs, Segment{Text: p.text})
		p.text = nil
	}
	p.segs = append(p.segs, Segment{Conflict: c})
}

func (p *parsed) done() []Segment {
	if len(p.text) > 0 {
		p.segs = append(p.segs, Segment{Text: p.text})
	}
	return p.segs
}

// lineAt returns the line of data that starts at pos, with its newline,
// and where the next one starts.
func lineAt(data []byte, pos int) ([]byte, int) {
	end := bytes.IndexByte(data[pos:], '\n')
	if end < 0 {
		return data[pos:], len(data)
	}
	return data[pos : pos+end+1], pos + end + 1
}

// conflictAt reads the conflict whose open marker is the line at pos,
// returning it, where its close marker ends, and the lines of its other
// markers; nil when no close marker ends it.
func conflictAt(data []byte, pos, size int) (*Conflict, int, [][]byte) {
	line, pos := lineAt(data, pos)
	_, n := markerOf(line, size)
	c := &Conflict{OursLabel: label(line, n), Ours: []byte{}, Theirs: []byte{}}
	marks := [][]byte{line}
	state := open // the last marker seen
	for pos < len(data) {
		line, pos = lineAt(data, pos)
		kind, n := markerOf(line, size)
		switch {
		case kind == baseMark && state == open:
			c.HasBase, c.BaseLabel, c.Base, state = true, label(line, n), []byte{}, baseMark
			marks = append(marks, line)
		case kind == sep && (state == open || state == baseMark):
			state = sep
			marks = append(marks, line)
		case kind == closeMark && state == sep:
			c.TheirsLabel = label(line, n)
			return c, pos, append(marks, line)
		case state == open:
			c.Ours = append(c.Ours, line...)
		case state == baseMark:
			c.Base = append(c.Base, line...)
		default:
			c.Theirs = append(c.Theirs, line...)
		}
	}
	return nil, pos, nil
}

// Count is the number of conflicts in segs.
func Count(segs []Segment) int {
	n := 0
	for _, s := range segs {
		if s.Conflict != nil {
			n++
		}
	}
	return n
}

// Resolve writes the file with conflict i resolved as choices[i] (custom[i]
// for Custom). It fails while a conflict is unresolved.
func Resolve(segs []Segment, choices []Choice, custom [][]byte) ([]byte, error) {
	var out []byte
	i := 0
	for _, s := range segs {
		c := s.Conflict
		if c == nil {
			out = append(out, s.Text...)
			continue
		}
		if i >= len(choices) {
			return nil, fmt.Errorf("conflict: conflict %d is not resolved", i+1)
		}
		switch choices[i] {
		case Ours:
			out = append(out, c.Ours...)
		case Theirs:
			out = append(out, c.Theirs...)
		case OursThenTheirs:
			out = append(append(out, endLine(c.Ours, c.Theirs)...), c.Theirs...)
		case TheirsThenOurs:
			out = append(append(out, endLine(c.Theirs, c.Ours)...), c.Ours...)
		case Base:
			out = append(out, c.Base...)
		case Custom:
			if i < len(custom) {
				out = append(out, custom[i]...)
			}
		default:
			return nil, fmt.Errorf("conflict: conflict %d is not resolved", i+1)
		}
		i++
	}
	return out, nil
}

// endLine is first with a line ending added when it lacks one and more
// text follows, in the ending next uses.
func endLine(first, next []byte) []byte {
	if len(first) == 0 || first[len(first)-1] == '\n' || len(next) == 0 {
		return first
	}
	if bytes.Contains(next, []byte("\r\n")) {
		return append(append([]byte(nil), first...), '\r', '\n')
	}
	return append(append([]byte(nil), first...), '\n')
}
