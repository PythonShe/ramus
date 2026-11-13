package diff

// Runs returns file f's runs of changed lines, in order, as ranges of
// Lines: lines - and + with no unchanged line between them. A viewer that
// takes the patch's changes as they are numbers its changes so.
func (p *Parsed) Runs(f int) []Span {
	var out []Span
	fl := p.Files[f]
	for h := fl.Hunks.Start; h < fl.Hunks.End; h++ {
		lines := p.Hunks[h].Lines
		open := false
		for i := lines.Start; i < lines.End; i++ {
			switch p.Lines[i].Kind {
			case '-', '+':
				if !open {
					out = append(out, Span{i, i + 1})
					open = true
				}
				out[len(out)-1].End = i + 1
			case ' ':
				open = false
			}
		}
	}
	return out
}

// SideLines maps the lines of file f's two sides to their lines in Lines,
// -1 where a line is not in the patch. When whole, a side is the file,
// numbered from 0 as in it; else it is the lines of the hunks one after
// another, as a viewer shows a patch alone.
func (p *Parsed) SideLines(f int, whole bool) (old, new []int32) {
	fl := p.Files[f]
	put := func(side []int32, at int32, i uint32) []int32 {
		for int32(len(side)) <= at {
			side = append(side, -1)
		}
		side[at] = int32(i)
		return side
	}
	var o, n int32
	for h := fl.Hunks.Start; h < fl.Hunks.End; h++ {
		lines := p.Hunks[h].Lines
		for i := lines.Start; i < lines.End; i++ {
			l := p.Lines[i]
			if l.Kind == '\\' {
				continue
			}
			if l.Kind != '+' {
				if whole {
					o = l.Old - 1
				}
				old = put(old, o, i)
				o++
			}
			if l.Kind != '-' {
				if whole {
					n = l.New - 1
				}
				new = put(new, n, i)
				n++
			}
		}
	}
	return old, new
}

// LineEndings says how file f's line endings changed when every line
// removed ended one way and every line added the other: "CRLF to LF" or
// "LF to CRLF", else "". Such a patch changes every line it touches with
// nothing to see in them.
func (p *Parsed) LineEndings(f int) string {
	var oldN, oldCR, newN, newCR int
	fl := p.Files[f]
	for h := fl.Hunks.Start; h < fl.Hunks.End; h++ {
		lines := p.Hunks[h].Lines
		for i := lines.Start; i < lines.End; i++ {
			l := p.Lines[i]
			cr := l.Text.End > l.Text.Start && p.Text[l.Text.End-1] == '\r'
			switch l.Kind {
			case '-':
				oldN++
				if cr {
					oldCR++
				}
			case '+':
				newN++
				if cr {
					newCR++
				}
			}
		}
	}
	switch {
	case oldN == 0 || newN == 0:
		return ""
	case oldCR == oldN && newCR == 0:
		return "CRLF to LF"
	case oldCR == 0 && newCR == newN:
		return "LF to CRLF"
	}
	return ""
}
