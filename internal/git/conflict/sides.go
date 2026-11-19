package conflict

import (
	"bytes"
	"errors"
)

// mergedSides are the versions of a conflicted file git merged, as the index
// holds them: the merge base (stage 1), ours (2) and theirs (3); nil for
// one that does not exist.
type mergedSides struct {
	Base, Ours, Theirs []byte
}

// ErrAmbiguous is a file whose conflicts cannot be told apart from text in
// it that looks like conflict markers.
var ErrAmbiguous = errors.New("conflict: text in the file looks like conflict markers, so its conflicts cannot be told apart; set a conflict-marker-size attribute for it and merge again, or resolve it in an editor")

// holdsMarkers reports whether a side holds a line like a conflict marker
// of size: when none does, every marker in the merge is git's.
func (s mergedSides) holdsMarkers(size int) bool {
	for _, side := range [][]byte{s.Base, s.Ours, s.Theirs} {
		for pos := 0; pos < len(side); {
			line, next := lineAt(side, pos)
			if kind, _ := markerOf(line, size); kind != none {
				return true
			}
			pos = next
		}
	}
	return false
}

// hold reports whether every line of data like a conflict marker of size
// can be text of the sides: none is in data more times than in the sides
// together. A merge takes each line it writes, but its markers, from one
// side, once.
func (s mergedSides) hold(data []byte, size int) bool {
	count := func(b []byte, add int, n map[string]int) {
		for pos := 0; pos < len(b); {
			line, next := lineAt(b, pos)
			pos = next
			if kind, _ := markerOf(line, size); kind != none {
				// Line endings aside: a checkout may write CRLF.
				n[string(bytes.TrimRight(line, "\r\n"))] += add
			}
		}
	}
	n := map[string]int{}
	count(data, 1, n)
	for _, side := range [][]byte{s.Base, s.Ours, s.Theirs} {
		count(side, -1, n)
	}
	for _, k := range n {
		if k > 0 {
			return false
		}
	}
	return true
}

// ending is how a side's version ends: in a last line with no line ending,
// or not, and with how many carriage returns.
type ending struct {
	open bool
	crs  int
}

func endingOf(b []byte) ending {
	return ending{open: len(b) > 0 && b[len(b)-1] != '\n', crs: len(b) - len(bytes.TrimRight(b, "\r"))}
}

// trimAdded takes off each side of the conflict that ends segs, when one
// does, the line ending git added to it: git ends every side of a conflict
// with one, for the marker after it, and so adds one where the side's
// version ends in a last line that has none, which is as the version ends
// once taken whole (git checkout --ours). The ending added is CRLF when
// the lines around are, or a checkout writes CRLF: a carriage return goes
// with it only when the version does not end with that one.
func trimAdded(segs []Segment, ours, base, theirs ending) {
	if len(segs) == 0 || segs[len(segs)-1].Conflict == nil {
		return
	}
	c := segs[len(segs)-1].Conflict
	trim := func(side []byte, e ending) []byte {
		if !e.open || !bytes.HasSuffix(side, []byte{'\n'}) {
			return side
		}
		side = side[: len(side)-1 : len(side)-1]
		if len(side)-len(bytes.TrimRight(side, "\r")) > e.crs {
			side = side[: len(side)-1 : len(side)-1]
		}
		return side
	}
	c.Ours, c.Theirs = trim(c.Ours, ours), trim(c.Theirs, theirs)
	if c.HasBase {
		c.Base = trim(c.Base, base)
	}
}
