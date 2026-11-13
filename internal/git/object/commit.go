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
	Message  []byte
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
		}
	}
	// A commit with headers and no blank line has an empty message.
	if c.Tree == nil {
		return c, ErrBadCommit
	}
	return c, nil
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
// as git's %s does.
func Subject(msg []byte) []byte {
	msg = bytes.TrimLeft(msg, "\n")
	end := bytes.Index(msg, []byte("\n\n"))
	if end >= 0 {
		msg = msg[:end]
	}
	msg = bytes.TrimRight(msg, "\n")
	if bytes.IndexByte(msg, '\n') < 0 {
		return msg
	}
	return bytes.ReplaceAll(msg, []byte{'\n'}, []byte{' '})
}
