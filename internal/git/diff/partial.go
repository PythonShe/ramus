package diff

import (
	"errors"
	"fmt"
)

// ErrNothingChosen is a selection with no added or removed line in it.
var ErrNothingChosen = errors.New("diff: no changed line chosen")

// ErrNotPartial is a file that cannot be taken apart by line: binary, or
// with a quoted path.
var ErrNotPartial = errors.New("diff: this file can only be taken whole")

// Select returns a patch of file f holding only the chosen changed lines,
// for git apply. chosen is given indexes into Lines.
//
// Forward makes a patch to apply as it is, from the patch's old side:
// staging lines of an unstaged patch. Lines not chosen stay as on the old
// side: an added one is left out, a removed one becomes context.
//
// Reverse makes a patch to apply with -R onto the patch's new side:
// unstaging lines of a staged patch, or discarding lines of an unstaged
// one. Lines not chosen stay as on the new side: an added one becomes
// context, a removed one is left out.
func (p *Parsed) Select(f int, reverse bool, chosen func(line int) bool) ([]byte, error) {
	fl := p.Files[f]
	if fl.Binary || fl.Quoted {
		return nil, ErrNotPartial
	}
	out := append([]byte(nil), p.Bytes(fl.Header)...)
	// delta is how far the side being rebuilt has moved from the kept
	// side, through the hunks written so far.
	var delta int32
	any := false
	for hi := fl.Hunks.Start; hi < fl.Hunks.End; hi++ {
		h := p.Hunks[hi]
		var body []byte
		var kept, built int32 // line counts of the kept and rebuilt sides
		changed := false
		// dropped is set when the last line was left out, so the "\ No
		// newline" after it goes too.
		dropped := false
		for li := h.Lines.Start; li < h.Lines.End; li++ {
			l := p.Lines[li]
			kind := l.Kind
			switch kind {
			case '\\':
				if !dropped {
					body = appendLine(body, '\\', p.Bytes(l.Text))
				}
				continue
			case ' ':
				kept++
				built++
			case '+', '-':
				// The side kept whole: old for forward, new for reverse.
				keptKind := byte('-')
				if reverse {
					keptKind = '+'
				}
				switch {
				case chosen(int(li)):
					changed = true
					if kind == keptKind {
						kept++
					} else {
						built++
					}
				case kind == keptKind:
					kind = ' '
					kept++
					built++
				default:
					dropped = true
					continue
				}
			}
			dropped = false
			body = appendLine(body, kind, p.Bytes(l.Text))
		}
		if !changed {
			continue
		}
		any = true
		keptStart, keptCount := h.OldStart, h.OldCount
		if reverse {
			keptStart, keptCount = h.NewStart, h.NewCount
		}
		if kept != keptCount {
			return nil, fmt.Errorf("%w: hunk %d does not add up", ErrBadPatch, hi-fl.Hunks.Start)
		}
		// A start is the first line of the hunk, or with a count of 0 the
		// line before it.
		before := keptStart - 1
		if keptCount == 0 {
			before = keptStart
		}
		builtBefore := before + delta
		if reverse {
			builtBefore = before - delta
		}
		builtStart := builtBefore + 1
		if built == 0 {
			builtStart = builtBefore
		}
		oldStart, oldCount, newStart, newCount := keptStart, kept, builtStart, built
		if reverse {
			oldStart, oldCount, newStart, newCount = builtStart, built, keptStart, kept
		}
		out = fmt.Appendf(out, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
		out = append(out, body...)
		if reverse {
			delta += kept - built
		} else {
			delta += built - kept
		}
	}
	if !any {
		return nil, ErrNothingChosen
	}
	return out, nil
}

func appendLine(b []byte, kind byte, text []byte) []byte {
	b = append(b, kind)
	b = append(b, text...)
	return append(b, '\n')
}
