package diff

import (
	"bytes"
	"errors"
	"fmt"

	"ramus/internal/git/lfs"
)

// ErrNothingChosen is a selection with no added or removed line in it.
var ErrNothingChosen = errors.New("diff: no changed line chosen")

// ErrNotPartial is a file that cannot be taken apart by line: binary, or
// with a quoted path; a Git LFS pointer; or, in one direction, a file the
// patch adds or deletes (see Partial).
var ErrNotPartial = errors.New("diff: this file can only be taken whole")

// Partial reports whether lines of file f can be chosen one by one in
// the direction Select is given, ErrNotPartial when not. git applies a
// patch that adds or deletes a file only whole: lines of a new file
// cannot be taken back one by one, nor lines of a deleted one taken; all
// of them at once can. The same holds both ways for a file stored in Git
// LFS, whose patch is of its pointer.
func (p *Parsed) Partial(f int, reverse bool) error {
	fl := p.Files[f]
	if fl.Binary || fl.Quoted || onlyWhole(p.Bytes(fl.Header), reverse) || p.pointer(fl) {
		return ErrNotPartial
	}
	return nil
}

// pointer reports whether fl's patch is of a Git LFS pointer, on either
// side: a pointer's version line as its first line, or an oid line
// changed, as a patch with no context shows it. Some of a pointer's lines
// make no pointer: git-lfs would write that text into the work tree in
// place of the file's content, or the index would get a broken pointer.
func (p *Parsed) pointer(fl File) bool {
	for hi := fl.Hunks.Start; hi < fl.Hunks.End; hi++ {
		h := p.Hunks[hi]
		for li := h.Lines.Start; li < h.Lines.End; li++ {
			l := p.Lines[li]
			text := p.Bytes(l.Text)
			if (l.Old == 1 || l.New == 1) && string(text) == lfs.Version ||
				(l.Kind == '+' || l.Kind == '-') && lfs.IsOIDLine(text) {
				return true
			}
		}
	}
	return false
}

func onlyWhole(header []byte, reverse bool) bool {
	mark := []byte("deleted file mode ")
	if reverse {
		mark = []byte("new file mode ")
	}
	for line := range bytes.Lines(header) {
		if bytes.HasPrefix(line, mark) {
			return true
		}
	}
	return false
}

// lineHeader is a file's header for a patch of some of its lines: a
// change of mode is left out, as it is not one of the lines chosen.
func lineHeader(h []byte) []byte {
	var out []byte
	for line := range bytes.Lines(h) {
		if !bytes.HasPrefix(line, []byte("old mode ")) && !bytes.HasPrefix(line, []byte("new mode ")) {
			out = append(out, line...)
		}
	}
	return out
}

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
//
// A change of the file's mode is never part of it.
func (p *Parsed) Select(f int, reverse bool, chosen func(line int) bool) ([]byte, error) {
	fl := p.Files[f]
	if fl.Binary || fl.Quoted {
		return nil, ErrNotPartial
	}
	out := lineHeader(p.Bytes(fl.Header))
	// The side kept whole: old for forward, new for reverse. The other is
	// rebuilt from the lines chosen.
	keptKind, builtKind := byte('-'), byte('+')
	if reverse {
		keptKind, builtKind = '+', '-'
	}
	// emit is the kind line li goes into the patch as, false for none.
	emit := func(li uint32) (byte, bool) {
		l := p.Lines[li]
		switch {
		case l.Kind != '+' && l.Kind != '-', chosen(int(li)):
			return l.Kind, true
		case l.Kind == keptKind:
			return ' ', true
		}
		return 0, false
	}
	// delta is how far the side being rebuilt has moved from the kept
	// side, through the hunks written so far.
	var delta int32
	any, all := false, true
	for hi := fl.Hunks.Start; hi < fl.Hunks.End; hi++ {
		h := p.Hunks[hi]
		// builtAfter reports whether a line from li on goes on the
		// rebuilt side.
		builtAfter := func(li uint32) bool {
			for ; li < h.Lines.End; li++ {
				if k, ok := emit(li); ok && k != '\\' && k != keptKind {
					return true
				}
			}
			return false
		}
		var body []byte
		var kept, built int32 // line counts of the kept and rebuilt sides
		changed := false
		for li := h.Lines.Start; li < h.Lines.End; li++ {
			l := p.Lines[li]
			if l.Kind == '\\' {
				continue // written with the line before it
			}
			if l.Kind == '+' || l.Kind == '-' {
				if chosen(int(li)) {
					changed = true
				} else {
					all = false
				}
			}
			kind, ok := emit(li)
			if !ok {
				continue
			}
			if kind != builtKind {
				kept++
			}
			if kind != keptKind {
				built++
			}
			text := p.Bytes(l.Text)
			if li+1 >= h.Lines.End || p.Lines[li+1].Kind != '\\' {
				body = appendLine(body, kind, text)
				continue
			}
			marker := p.Bytes(p.Lines[li+1].Text)
			if kind == keptKind || !builtAfter(li+2) {
				body = appendLine(appendLine(body, kind, text), '\\', marker)
				continue
			}
			// The line ends its side with no newline, but the rebuilt
			// side goes on past it: there it needs one, or it would join
			// the line after it. As context, it is taken off the kept
			// side as it is and put on the rebuilt one with a newline.
			if kind == ' ' {
				body = appendLine(appendLine(body, keptKind, text), '\\', marker)
				kind = builtKind
			}
			body = appendLine(body, kind, text)
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
	if !all && (onlyWhole(p.Bytes(fl.Header), reverse) || p.pointer(fl)) {
		return nil, ErrNotPartial
	}
	return out, nil
}

func appendLine(b []byte, kind byte, text []byte) []byte {
	b = append(b, kind)
	b = append(b, text...)
	return append(b, '\n')
}
