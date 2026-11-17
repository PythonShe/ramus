package message

import (
	"bytes"
	"regexp"
	"strings"
)

// Trailer is one "Key: value" line of a message's trailer block, with the
// lines that continue it.
type Trailer struct {
	Key, Value string
	// Start and End are its bytes in the message, its newline included.
	Start, End int
}

// Person is a name and an email, as a trailer such as Co-authored-by
// names someone.
type Person struct{ Name, Email string }

var (
	trailerKey = regexp.MustCompile(`^[A-Za-z0-9-]+[ \t]*:`)
	namedRE    = regexp.MustCompile(`^\s*([^<>]*?)\s*<([^<>\s]+)>\s*$`)
)

// gitTrailerPrefixes are the lines git itself writes into a trailer block;
// one of them lets the block hold other lines, as git's trailer.c does.
var gitTrailerPrefixes = []string{"Signed-off-by: ", "(cherry picked from commit "}

// Trailers returns the trailers of commit message msg, read as git
// interpret-trailers --parse reads them: the last paragraph, not the
// subject's, whose lines are all trailers, or of which a quarter are
// when git wrote one of them. The message ends at a "---" line, where a
// patch would start.
func Trailers(msg []byte) []Trailer {
	end := len(msg)
	for at := 0; at < len(msg); {
		line, next := lineAt(msg, at)
		if rest, ok := bytes.CutPrefix(line, []byte("---")); ok && (len(rest) == 0 || isSpace(rest[0])) {
			end = at
			break
		}
		at = next
	}
	msg = msg[:end]
	// The lines of the last paragraph, after the subject's.
	type ln struct{ start, end int }
	var lines []ln
	seenText, blankAfterText := false, false
	for at := 0; at < len(msg); {
		line, next := lineAt(msg, at)
		if len(bytes.TrimSpace(line)) == 0 {
			if seenText {
				blankAfterText = true
			}
		} else {
			if blankAfterText {
				lines, blankAfterText = lines[:0], false
			}
			if seenText || len(lines) > 0 {
				lines = append(lines, ln{at, next})
			} else {
				// The subject paragraph is never the trailer block.
				lines = append(lines, ln{-1, -1})
			}
			seenText = true
		}
		at = next
	}
	if len(lines) == 0 || lines[0].start < 0 {
		return nil
	}
	var out []Trailer
	trailers, others, recognized := 0, 0, false
	// after is what the line before an indented one was, as git's
	// trailer.c tells: a trailer, which the line continues; a line git
	// wrote that is none, which takes it without showing it; or text,
	// with which it counts as text.
	const (
		afterText = iota
		afterTrailer
		afterGit
	)
	after := afterText
	for _, l := range lines {
		line := bytes.TrimRight(msg[l.start:l.end], "\r\n")
		git := false
		for _, p := range gitTrailerPrefixes {
			if bytes.HasPrefix(line, []byte(p)) {
				git, recognized = true, true
			}
		}
		switch {
		case len(line) > 0 && isSpace(line[0]):
			switch after {
			case afterTrailer:
				t := &out[len(out)-1]
				t.Value += " " + strings.TrimSpace(string(line))
				t.End = l.end
			case afterText:
				others++
			}
		case trailerKey.Match(line):
			k, v, _ := bytes.Cut(line, []byte(":"))
			out = append(out, Trailer{Key: string(bytes.TrimSpace(k)), Value: string(bytes.TrimSpace(v)), Start: l.start, End: l.end})
			trailers++
			after = afterTrailer
		case git:
			// A line git wrote counts toward the block, as a trailer
			// would, though it is none.
			trailers++
			after = afterGit
		default:
			others++
			after = afterText
		}
	}
	if trailers == 0 || others > 0 && !(recognized && trailers*3 >= others) {
		return nil
	}
	return out
}

// CoAuthors returns the people the Co-authored-by trailers of msg name.
func CoAuthors(msg []byte) []Person {
	var out []Person
	for _, t := range Trailers(msg) {
		if p, ok := CoAuthor(t); ok {
			out = append(out, p)
		}
	}
	return out
}

// CoAuthor returns the person t names when it is a Co-authored-by
// trailer that names one.
func CoAuthor(t Trailer) (Person, bool) {
	if !strings.EqualFold(t.Key, "Co-authored-by") {
		return Person{}, false
	}
	m := namedRE.FindStringSubmatch(t.Value)
	if m == nil {
		return Person{}, false
	}
	return Person{Name: m[1], Email: m[2]}, true
}

func lineAt(b []byte, at int) (line []byte, next int) {
	if i := bytes.IndexByte(b[at:], '\n'); i >= 0 {
		return b[at : at+i], at + i + 1
	}
	return b[at:], len(b)
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }
