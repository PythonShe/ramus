// Package object parses git's loose object formats once their bytes are in
// hand: commits for now.
package object

import (
	"bytes"
	"errors"
	"strconv"
)

// Signature is an author or committer line.
type Signature struct {
	Name, Email []byte
	// When is seconds since the epoch; Offset the recorded zone in minutes
	// east of UTC, so that a commit's local time can be shown as recorded.
	When   int64
	Offset int16
}

// Commit is a parsed commit. Every slice points into the bytes it was
// parsed from.
type Commit struct {
	Tree      []byte // hex
	Parents   int
	Author    Signature
	Committer Signature
	// Encoding is the commit's encoding header, empty for UTF-8.
	Encoding []byte
	Signed   bool // a gpgsig header is present
	// SigKind is the kind of key that signed it, read from the
	// signature's armor.
	SigKind SigKind
	Message []byte
}

// SigKind is the kind of key a signature was made with, as git's
// gpg.format names them.
type SigKind byte

const (
	SigUnknown SigKind = iota
	SigOpenPGP
	SigSSH
	SigX509
)

// sigKind reads the kind of a signature from its armor's first line.
func sigKind(armor []byte) SigKind {
	switch string(bytes.TrimSpace(armor)) {
	case "-----BEGIN PGP SIGNATURE-----":
		return SigOpenPGP
	case "-----BEGIN SSH SIGNATURE-----":
		return SigSSH
	case "-----BEGIN SIGNED MESSAGE-----":
		return SigX509
	}
	return SigUnknown
}

// ErrBadCommit reports bytes that are not a commit git would write.
var ErrBadCommit = errors.New("object: malformed commit")

// ParseCommit parses a commit object's contents.
func ParseCommit(data []byte) (Commit, error) {
	var c Commit
	rest := data
	for len(rest) > 0 {
		i := bytes.IndexByte(rest, '\n')
		if i < 0 {
			return c, ErrBadCommit
		}
		line := rest[:i]
		rest = rest[i+1:]
		if len(line) == 0 {
			c.Message = rest
			return c, nil
		}
		if line[0] == ' ' {
			continue // continuation of a multi-line header (gpgsig, mergetag)
		}
		key, val, _ := bytes.Cut(line, []byte{' '})
		switch string(key) {
		case "tree":
			c.Tree = val
		case "parent":
			c.Parents++
		case "author":
			c.Author = parseSignature(val)
		case "committer":
			c.Committer = parseSignature(val)
		case "encoding":
			c.Encoding = val
		case "gpgsig", "gpgsig-sha256":
			c.Signed = true
			c.SigKind = sigKind(val)
		}
	}
	// A commit with headers and no blank line has an empty message.
	if c.Tree == nil {
		return c, ErrBadCommit
	}
	return c, nil
}

// ParentIDs returns the parents a commit's headers name, as hex, in
// order; the slices point into data.
func ParentIDs(data []byte) [][]byte {
	var out [][]byte
	for rest := data; len(rest) > 0; {
		i := bytes.IndexByte(rest, '\n')
		if i <= 0 {
			break // end of the headers
		}
		line := rest[:i]
		rest = rest[i+1:]
		if v, ok := bytes.CutPrefix(line, []byte("parent ")); ok {
			out = append(out, v)
		}
	}
	return out
}

// parseSignature reads "Name <email> 1700000000 +0100". git tolerates
// damaged lines in old history, so this does too: what cannot be read is
// left empty.
func parseSignature(b []byte) Signature {
	var s Signature
	lt := bytes.IndexByte(b, '<')
	gt := bytes.LastIndexByte(b, '>')
	if lt < 0 || gt < lt {
		s.Name = bytes.TrimSpace(b)
		return s
	}
	s.Name = bytes.TrimSpace(b[:lt])
	s.Email = b[lt+1 : gt]
	// "<seconds> <+hhmm>", read by hand: splitting a hostile tail into
	// fields would allocate per space.
	when, zone, _ := bytes.Cut(bytes.TrimLeft(b[gt+1:], " "), []byte{' '})
	if len(when) <= 20 {
		s.When, _ = strconv.ParseInt(string(when), 10, 64)
	}
	zone = bytes.TrimSpace(zone)
	if len(zone) == 5 && (zone[0] == '+' || zone[0] == '-') {
		hh, err1 := strconv.Atoi(string(zone[1:3]))
		mm, err2 := strconv.Atoi(string(zone[3:5]))
		if err1 == nil && err2 == nil {
			off := int16(hh*60 + mm)
			if zone[0] == '-' {
				off = -off
			}
			s.Offset = off
		}
	}
	return s
}

// Subject returns the first paragraph of a message joined into one line,
// as git's %s does: blank lines before it are skipped, a line of white
// space alone ends it (so CRLF messages split as LF ones do), and each
// line loses its trailing white space. A one-line subject points into msg.
func Subject(msg []byte) []byte {
	var first, joined []byte
	started := false
	for len(msg) > 0 {
		line := msg
		if i := bytes.IndexByte(msg, '\n'); i >= 0 {
			line, msg = msg[:i], msg[i+1:]
		} else {
			msg = nil
		}
		line = bytes.TrimRight(line, gitSpace)
		if len(line) == 0 {
			if started {
				break
			}
			continue
		}
		switch {
		case !started:
			first, started = line, true
		case joined == nil:
			joined = append(append(append(joined, first...), ' '), line...)
		default:
			joined = append(append(joined, ' '), line...)
		}
	}
	if joined != nil {
		return joined
	}
	return first
}

// gitSpace is what git's isspace takes for white space.
const gitSpace = " \t\n\r"
