// Package textenc turns the text of commits into UTF-8 and back: a
// message in the encoding its commit names, and bytes that are not UTF-8
// shown as escapes instead of lost.
package textenc

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/encoding/ianaindex"
)

// IsUTF8 reports whether enc, an encoding a commit or the config names,
// is UTF-8; "" is, as git takes it.
func IsUTF8(enc string) bool {
	switch strings.ToLower(strings.TrimSpace(enc)) {
	case "", "utf-8", "utf8":
		return true
	}
	return false
}

// lookup finds the encoding git, through iconv, would take name for.
func lookup(name string) (encoding.Encoding, error) {
	if e, err := ianaindex.IANA.Encoding(name); err == nil && e != nil {
		return e, nil
	}
	if e, err := htmlindex.Get(name); err == nil {
		return e, nil
	}
	return nil, fmt.Errorf("textenc: unknown encoding %q", name)
}

// Decode returns b, in the encoding enc names, as UTF-8. ok is false when
// b is not valid in it, or enc is not one known here: the bytes are then
// kept, each one not UTF-8 written as \xNN, so nothing is lost.
func Decode(b []byte, enc string) (string, bool) {
	if !IsUTF8(enc) {
		if e, err := lookup(enc); err == nil {
			if out, err := e.NewDecoder().Bytes(b); err == nil && utf8.Valid(out) {
				return string(out), true
			}
		}
	}
	if utf8.Valid(b) {
		return string(b), IsUTF8(enc)
	}
	return Escape(b), false
}

// Escape writes b as UTF-8, each byte that is not part of a valid UTF-8
// sequence as \xNN.
func Escape(b []byte) string {
	var s strings.Builder
	for len(b) > 0 {
		r, n := utf8.DecodeRune(b)
		if r == utf8.RuneError && n <= 1 {
			fmt.Fprintf(&s, `\x%02x`, b[0])
			b = b[1:]
			continue
		}
		s.Write(b[:n])
		b = b[n:]
	}
	return s.String()
}

// ErrUnrepresentable is text with characters the encoding cannot hold.
var ErrUnrepresentable = errors.New("textenc: the text has characters the encoding cannot hold")

// Encode returns s, UTF-8, in the encoding enc names, as git expects a
// commit message once i18n.commitEncoding names it.
func Encode(s, enc string) ([]byte, error) {
	if IsUTF8(enc) {
		return []byte(s), nil
	}
	e, err := lookup(enc)
	if err != nil {
		return nil, err
	}
	out, err := e.NewEncoder().Bytes([]byte(s))
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrUnrepresentable, enc)
	}
	// Encoders replace what they cannot hold; going back must give s.
	if back, err := e.NewDecoder().Bytes(out); err != nil || string(back) != s {
		return nil, fmt.Errorf("%w: %s", ErrUnrepresentable, enc)
	}
	return out, nil
}
