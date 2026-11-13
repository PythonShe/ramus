package diff

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
)

// Parsed is a unified patch split into files, hunks and lines. It points
// into Text by offsets, so a patch of any size is four flat slices and no
// object per line.
type Parsed struct {
	Text  []byte
	Files []File
	Hunks []Hunk
	Lines []Line
	// Cut is set when the patch had more than MaxLines lines; the rest is
	// not taken apart.
	Cut bool
}

// Span is a range of bytes in Text, or of entries in Hunks or Lines.
type Span struct{ Start, End uint32 }

// File is one file's part of the patch.
type File struct {
	// Header covers the lines from "diff --git" up to the first hunk.
	Header Span
	// Old and New are the paths after "--- a/" and "+++ b/", empty for
	// /dev/null.
	Old, New Span
	// Quoted is set when git quoted a path (it holds a control character,
	// a quote or a backslash); such a file is shown, not taken apart.
	Quoted bool
	Binary bool
	Hunks  Span
}

// Hunk is one "@@" section.
type Hunk struct {
	OldStart, OldCount, NewStart, NewCount int32
	// Header is the "@@ ... @@" line, the function context included.
	Header Span
	Lines  Span
}

// Line is one line of a hunk.
type Line struct {
	// Text is the line without its marker and line end.
	Text Span
	// Kind is ' ', '+', '-', or '\\' for "\ No newline at end of file".
	Kind byte
	// Old and New are the line's numbers on each side, 0 where it is not.
	Old, New int32
}

// MaxLines bounds the lines Parse takes apart; a patch with more is cut
// there and Parsed.Cut reports it.
const MaxLines = 4 << 20

// ErrBadPatch is a patch Parse cannot read.
var ErrBadPatch = errors.New("diff: patch not understood")

// Parse splits a patch from WorkTreePatch or CommitPatch.
func Parse(text []byte) (*Parsed, error) {
	p := &Parsed{Text: text}
	var f *File
	var h *Hunk
	var oldNo, newNo, oldLeft, newLeft int32
	endHunk := func() {
		if h != nil {
			h.Lines.End = uint32(len(p.Lines))
			h = nil
		}
	}
	endFile := func() {
		endHunk()
		if f != nil {
			f.Hunks.End = uint32(len(p.Hunks))
			f = nil
		}
	}
	for off := 0; off < len(text); {
		end := bytes.IndexByte(text[off:], '\n')
		next := off + end + 1
		if end < 0 {
			end, next = len(text)-off, len(text)
		}
		line := text[off : off+end]
		start := uint32(off)
		off = next
		if h != nil && (oldLeft > 0 || newLeft > 0 || (len(line) > 0 && line[0] == '\\')) {
			if len(p.Lines) >= MaxLines {
				p.Cut = true
				break
			}
			l := Line{Text: Span{start + 1, start + uint32(len(line))}}
			if len(line) == 0 {
				// An empty context line, from a patch made with
				// diff.suppressBlankEmpty.
				l.Kind, l.Text = ' ', Span{start, start}
			} else {
				l.Kind = line[0]
			}
			switch l.Kind {
			case ' ':
				l.Old, l.New = oldNo, newNo
				oldNo, newNo, oldLeft, newLeft = oldNo+1, newNo+1, oldLeft-1, newLeft-1
			case '-':
				l.Old = oldNo
				oldNo, oldLeft = oldNo+1, oldLeft-1
			case '+':
				l.New = newNo
				newNo, newLeft = newNo+1, newLeft-1
			case '\\':
			default:
				return nil, fmt.Errorf("%w: line %q in a hunk", ErrBadPatch, line)
			}
			if oldLeft < 0 || newLeft < 0 {
				return nil, fmt.Errorf("%w: hunk longer than its header says", ErrBadPatch)
			}
			p.Lines = append(p.Lines, l)
			continue
		}
		endHunk()
		switch {
		case bytes.HasPrefix(line, []byte("diff --git ")) || bytes.HasPrefix(line, []byte("diff --cc ")) || bytes.HasPrefix(line, []byte("diff --combined ")):
			endFile()
			p.Files = append(p.Files, File{Header: Span{start, start}, Hunks: Span{uint32(len(p.Hunks)), 0}})
			f = &p.Files[len(p.Files)-1]
			f.Old, f.New = gitLinePaths(line, start)
		case f == nil:
			return nil, fmt.Errorf("%w: %q before any file", ErrBadPatch, line)
		case bytes.HasPrefix(line, []byte("@@ ")):
			hk, err := hunkHeader(line)
			if err != nil {
				return nil, err
			}
			hk.Header = Span{start, start + uint32(len(line))}
			hk.Lines.Start = uint32(len(p.Lines))
			p.Hunks = append(p.Hunks, hk)
			h = &p.Hunks[len(p.Hunks)-1]
			oldNo, newNo, oldLeft, newLeft = hk.OldStart, hk.NewStart, hk.OldCount, hk.NewCount
			if oldNo == 0 {
				oldNo = 1
			}
			if newNo == 0 {
				newNo = 1
			}
		case bytes.HasPrefix(line, []byte("--- ")):
			f.Old, f.Quoted = pathSpan(line, 4, start, "a/", f.Quoted)
		case bytes.HasPrefix(line, []byte("+++ ")):
			f.New, f.Quoted = pathSpan(line, 4, start, "b/", f.Quoted)
		case bytes.HasPrefix(line, []byte("Binary files ")) || bytes.Equal(line, []byte("GIT binary patch")):
			f.Binary = true
		}
		if f != nil && len(p.Hunks) == int(f.Hunks.Start) {
			f.Header.End = start + uint32(len(line)) + 1
			if int(f.Header.End) > len(text) {
				f.Header.End = uint32(len(text))
			}
		}
	}
	endFile()
	return p, nil
}

// hunkHeader reads "@@ -a,b +c,d @@".
func hunkHeader(line []byte) (Hunk, error) {
	var h Hunk
	rest := line[3:]
	parse := func(prefix byte) (start, count int32, err error) {
		if len(rest) == 0 || rest[0] != prefix {
			return 0, 0, fmt.Errorf("%w: hunk header %q", ErrBadPatch, line)
		}
		end := bytes.IndexByte(rest, ' ')
		if end < 0 {
			return 0, 0, fmt.Errorf("%w: hunk header %q", ErrBadPatch, line)
		}
		field := string(rest[1:end])
		rest = rest[end+1:]
		count = 1
		if s, c, ok := cutComma(field); ok {
			field = s
			n, err := strconv.ParseInt(c, 10, 32)
			if err != nil || n < 0 {
				return 0, 0, fmt.Errorf("%w: hunk header %q", ErrBadPatch, line)
			}
			count = int32(n)
		}
		n, err := strconv.ParseInt(field, 10, 32)
		if err != nil || n < 0 {
			return 0, 0, fmt.Errorf("%w: hunk header %q", ErrBadPatch, line)
		}
		return int32(n), count, nil
	}
	var err error
	if h.OldStart, h.OldCount, err = parse('-'); err != nil {
		return h, err
	}
	if h.NewStart, h.NewCount, err = parse('+'); err != nil {
		return h, err
	}
	if !bytes.HasPrefix(rest, []byte("@@")) {
		return h, fmt.Errorf("%w: hunk header %q", ErrBadPatch, line)
	}
	return h, nil
}

func cutComma(s string) (string, string, bool) {
	for i := range len(s) {
		if s[i] == ',' {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}

// pathSpan returns where the path of a "--- " or "+++ " line is, without
// its prefix; /dev/null is empty.
func pathSpan(line []byte, skip int, start uint32, prefix string, quoted bool) (Span, bool) {
	p := line[skip:]
	if bytes.Equal(p, []byte("/dev/null")) {
		return Span{}, quoted
	}
	if len(p) > 0 && p[0] == '"' {
		return Span{start + uint32(skip), start + uint32(len(line))}, true
	}
	if bytes.HasPrefix(p, []byte(prefix)) {
		skip += len(prefix)
	}
	// git ends the name with a tab when it holds a space at its end.
	end := len(line)
	if line[end-1] == '\t' {
		end--
	}
	return Span{start + uint32(skip), start + uint32(end)}, quoted
}

// gitLinePaths reads the paths of "diff --git a/X b/X" when the two are
// the same, as for a binary file, which has no "---" and "+++" lines; a
// rename or a name with " b/" in it is left to those lines.
func gitLinePaths(line []byte, start uint32) (Span, Span) {
	const skip = len("diff --git ")
	rest := line[min(skip, len(line)):]
	n := len(rest)
	if n < 7 || n%2 == 0 || !bytes.HasPrefix(rest, []byte("a/")) {
		return Span{}, Span{}
	}
	half := (n - 1) / 2
	a, b := rest[:half], rest[half+1:]
	if rest[half] != ' ' || !bytes.HasPrefix(b, []byte("b/")) || !bytes.Equal(a[2:], b[2:]) {
		return Span{}, Span{}
	}
	at := start + uint32(skip)
	return Span{at + 2, at + uint32(half)}, Span{at + uint32(half) + 3, at + uint32(n)}
}

// Bytes returns the text of a span.
func (p *Parsed) Bytes(s Span) []byte { return p.Text[s.Start:s.End] }

// Path returns the file's path: the new one, or the old one for a file
// deleted.
func (p *Parsed) Path(f int) string {
	if fl := p.Files[f]; fl.New.End > fl.New.Start {
		return string(p.Bytes(fl.New))
	}
	return string(p.Bytes(p.Files[f].Old))
}

// Size is the memory the parsed form holds besides the text.
func (p *Parsed) Size() int {
	return len(p.Files)*48 + len(p.Hunks)*40 + len(p.Lines)*20
}
