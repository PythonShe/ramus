package conflict

import (
	"bytes"
	"errors"
)

// Sides are the versions of a conflicted file git merged, as the index
// holds them: the merge base (stage 1), ours (2) and theirs (3); nil for
// one that does not exist.
type Sides struct {
	Base, Ours, Theirs []byte
}

// ErrAmbiguous is a file whose conflicts cannot be told apart from text in
// it that looks like conflict markers.
var ErrAmbiguous = errors.New("conflict: text in the file looks like conflict markers, so its conflicts cannot be told apart; set a conflict-marker-size attribute for it and merge again, or resolve it in an editor")

// checkBudget bounds the bytes ParseSides reads again to tell text from
// conflicts, for a file full of lines that look like markers.
const checkBudget = 1 << 30

// ParseSides is ParseSize for a file git left conflicted, told apart from
// text in it that only looks like a conflict, as a document's example of
// one, by the sides that were merged: what a side holds as it is is text,
// never a conflict.
//
// When no side holds a line like a marker of size, every marker in data
// is git's and ParseSides is ParseSize. When one does:
//   - a block from an open marker to its close that a side holds whole is
//     text;
//   - a conflict in which a side's marker line takes part, or whose sides
//     hold marker lines, must be what the sides hold: its ours a run of
//     stage 2, its theirs of stage 3, its base of stage 1. When it is not
//     and it opens on a line a side holds, that line is text and reading
//     goes on after it;
//   - an open marker no close follows is text when a side holds it;
//   - each line of the merge comes from a side, so the text and ours and
//     theirs of the conflicts hold a marker line no more often than ours
//     and theirs together, and the bases no more often than the base: one
//     more is one of git's markers misread.
//
// What cannot be told apart is ErrAmbiguous, not a guess that, once
// resolved, could drop lines that were not in a conflict.
func ParseSides(data []byte, size int, s Sides) ([]Segment, error) {
	if len(data) > MaxSize {
		return nil, errLarge
	}
	// sides counts the marker lines of ours and theirs, base those of the
	// merge base.
	sides, base := map[string]int{}, map[string]int{}
	countMarkers(sides, s.Ours, size, 1)
	countMarkers(sides, s.Theirs, size, 1)
	countMarkers(base, s.Base, size, 1)
	if len(sides) == 0 && len(base) == 0 {
		return ParseSize(data, size)
	}
	held := func(line []byte) bool {
		k := markerKey(line)
		return sides[k] > 0 || base[k] > 0
	}
	budget := checkBudget
	spend := func(n int) bool {
		budget -= n
		return budget >= 0
	}
	// holds reports whether a side holds text as a run of its lines.
	holds := func(side, text []byte) bool {
		if !spend(2 * len(side)) {
			return false
		}
		return holdsRun(side, text)
	}
	var p parsed
	for pos := 0; pos < len(data); {
		line, next := lineAt(data, pos)
		if kind, _ := markerOf(line, size); kind != open {
			p.text = append(p.text, line...)
			pos = next
			continue
		}
		fake := held(line)
		c, end, marks := conflictAt(data, pos, size)
		if !spend(end - pos) {
			return nil, ErrAmbiguous
		}
		if c == nil {
			if !fake {
				return nil, ErrMarkers
			}
			// No open marker after this one closes either: the machine
			// that read from here met every marker after it.
			p.text = append(p.text, data[pos:]...)
			break
		}
		if fake {
			block := data[pos:end]
			if holds(s.Ours, block) || holds(s.Theirs, block) || holds(s.Base, block) {
				p.text = append(p.text, block...)
				pos = end
				continue
			}
		}
		suspect := hasMarker(c.Ours, size) || hasMarker(c.Base, size) || hasMarker(c.Theirs, size)
		for _, m := range marks {
			suspect = suspect || held(m)
		}
		if suspect && !(holds(s.Ours, c.Ours) && holds(s.Theirs, c.Theirs) && (!c.HasBase || holds(s.Base, c.Base))) {
			if !fake || budget < 0 {
				return nil, ErrAmbiguous
			}
			p.text = append(p.text, line...)
			pos = next
			continue
		}
		p.add(c)
		pos = end
	}
	segs := p.done()
	for _, sg := range segs {
		if c := sg.Conflict; c != nil {
			countMarkers(sides, c.Ours, size, -1)
			countMarkers(sides, c.Theirs, size, -1)
			countMarkers(base, c.Base, size, -1)
		} else {
			countMarkers(sides, sg.Text, size, -1)
		}
	}
	for _, m := range []map[string]int{sides, base} {
		for _, n := range m {
			if n < 0 {
				return nil, ErrAmbiguous
			}
		}
	}
	return segs, nil
}

// countMarkers adds by to m for each marker line of text, by its key.
func countMarkers(m map[string]int, text []byte, size, by int) {
	for pos := 0; pos < len(text); {
		line, next := lineAt(text, pos)
		if kind, _ := markerOf(line, size); kind != none {
			m[markerKey(line)] += by
		}
		pos = next
	}
}

// markerKey is a marker line without its line ending, which the work
// tree may have in CRLF where the index has LF.
func markerKey(line []byte) string {
	line = bytes.TrimSuffix(line, []byte{'\n'})
	return string(bytes.TrimSuffix(line, []byte{'\r'}))
}

func hasMarker(text []byte, size int) bool {
	for pos := 0; pos < len(text); {
		line, next := lineAt(text, pos)
		if kind, _ := markerOf(line, size); kind != none {
			return true
		}
		pos = next
	}
	return false
}

// holdsRun reports whether side holds text as a run of its lines: at a
// line's start, ending at a line's end. The work tree's text may end in
// CRLF where the index has LF, and git ends a side's last line in a
// conflict when the side did not.
func holdsRun(side, text []byte) bool {
	if len(text) == 0 {
		return true
	}
	if runAt(side, text) {
		return true
	}
	if lf := bytes.ReplaceAll(text, []byte("\r\n"), []byte("\n")); len(lf) != len(text) && runAt(side, lf) {
		return true
	}
	return false
}

func runAt(side, text []byte) bool {
	if text[len(text)-1] != '\n' {
		// Text that ends the file without a newline ends the side.
		return endsWith(side, text)
	}
	if bytes.HasPrefix(side, text) || bytes.Contains(side, append([]byte{'\n'}, text...)) {
		return true
	}
	// The side's last line, without the newline git gave it.
	trimmed := bytes.TrimSuffix(text[:len(text)-1], []byte{'\r'})
	return len(trimmed) > 0 && endsWith(side, trimmed)
}

// endsWith reports whether side ends with text, from a line's start.
func endsWith(side, text []byte) bool {
	i := len(side) - len(text)
	return bytes.HasSuffix(side, text) && (i == 0 || side[i-1] == '\n')
}
