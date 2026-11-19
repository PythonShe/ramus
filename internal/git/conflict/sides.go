package conflict

import "errors"

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
