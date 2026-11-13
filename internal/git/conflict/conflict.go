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

// markerOf reports whether line is a conflict marker, of what kind and
// size: at least 7 of one character, then a space or the line's end.
// size, when not 0, is the size an open marker set.
func markerOf(line []byte, size int) (marker, int) {
	if len(line) < 7 {
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
	if n < 7 || size != 0 && n != size {
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

// Parse splits data into segments. A marker out of place is read as text
// of the side it is in; Parse fails when a conflict is not closed, or the
// file is larger than MaxSize.
func Parse(data []byte) ([]Segment, error) {
	if len(data) > MaxSize {
		return nil, fmt.Errorf("conflict: the file is larger than %d MB", MaxSize>>20)
	}
	var segs []Segment
	var text []byte
	var cur *Conflict
	state := none // the last marker seen inside a conflict
	size := 0
	start := 0
	for start < len(data) {
		end := bytes.IndexByte(data[start:], '\n')
		if end < 0 {
			end = len(data)
		} else {
			end += start + 1
		}
		line := data[start:end]
		start = end
		kind, n := markerOf(line, size)
		if cur == nil {
			if kind == open {
				if len(text) > 0 {
					segs = append(segs, Segment{Text: text})
					text = nil
				}
				cur = &Conflict{OursLabel: label(line, n), Ours: []byte{}, Theirs: []byte{}}
				state, size = open, n
				continue
			}
			// A line like a marker outside a conflict, as a reStructuredText
			// underline, is text.
			text = append(text, line...)
			continue
		}
		switch {
		case kind == baseMark && state == open:
			cur.HasBase, cur.BaseLabel, cur.Base, state = true, label(line, n), []byte{}, baseMark
		case kind == sep && (state == open || state == baseMark):
			state = sep
		case kind == closeMark && state == sep:
			cur.TheirsLabel = label(line, n)
			segs = append(segs, Segment{Conflict: cur})
			cur, state, size = nil, none, 0
		case state == open:
			cur.Ours = append(cur.Ours, line...)
		case state == baseMark:
			cur.Base = append(cur.Base, line...)
		default:
			cur.Theirs = append(cur.Theirs, line...)
		}
	}
	if cur != nil {
		return nil, ErrMarkers
	}
	if len(text) > 0 {
		segs = append(segs, Segment{Text: text})
	}
	return segs, nil
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
