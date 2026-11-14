// Package redact makes text from git safe to show and to keep: it strips
// terminal escape sequences and control characters, hides credentials,
// and names the real host of a URL.
package redact

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Control removes what a terminal or a text view could misread: escape
// sequences (CSI, OSC and the rest), control characters other than
// newline and tab, C1 controls and bidirectional overrides, which can
// make a host or a path read as something else. A lone \r becomes a
// newline, as git's progress lines are meant to be read.
func Control(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == 0x1b:
			i = skipEscape(s, i)
			continue
		case c == '\r':
			if i+1 < len(s) && s[i+1] == '\n' {
				i++
				continue
			}
			b.WriteByte('\n')
		case c == '\n' || c == '\t':
			b.WriteByte(c)
		case c < 0x20 || c == 0x7f:
		case c < utf8.RuneSelf:
			b.WriteByte(c)
		default:
			r, n := utf8.DecodeRuneInString(s[i:])
			switch {
			case r == utf8.RuneError && n == 1:
				// Kept, as an escape, so that text that is not UTF-8
				// loses nothing.
				fmt.Fprintf(&b, `\x%02x`, c)
			case r == 0x9b: // C1 CSI
				i = skipCSI(s, i+n)
				continue
			case r >= 0x80 && r <= 0x9f:
			case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069, r == 0x200e, r == 0x200f:
			default:
				b.WriteString(s[i : i+n])
			}
			i += n
			continue
		}
		i++
	}
	return b.String()
}

// skipEscape returns the index after the escape sequence at i.
func skipEscape(s string, i int) int {
	i++
	if i >= len(s) {
		return i
	}
	switch s[i] {
	case '[':
		return skipCSI(s, i+1)
	case ']', 'P', '_', '^', 'X':
		// A string ends at BEL or ST (ESC \).
		for j := i + 1; j < len(s); j++ {
			if s[j] == 0x07 {
				return j + 1
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return j + 2
			}
		}
		return len(s)
	default:
		// ESC with intermediates, then one final byte.
		for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
			i++
		}
		return min(i+1, len(s))
	}
}

func skipCSI(s string, i int) int {
	for i < len(s) {
		c := s[i]
		i++
		if c >= 0x40 && c <= 0x7e {
			break
		}
	}
	return i
}

var (
	userinfoRE = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/@\s]+@`)
	headerRE   = regexp.MustCompile(`(?i)\b((?:proxy-)?authorization|cookie|set-cookie|private-token|x-api-key)(\s*[:=]\s*)[^\n]*`)
	pairRE     = regexp.MustCompile(`(?im)^(\s*(?:password|token|oauth_?refresh_?token|authtype|credential)\s*=).*$`)
	tokenRE    = regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|glpat-[A-Za-z0-9_-]{20,}|AKIA[0-9A-Z]{16}|xox[abposr]-[A-Za-z0-9-]{10,}|ATBB[A-Za-z0-9]{20,})\b`)
	extraRE    = regexp.MustCompile(`(?i)(http\.(?:[^\s=]*\.)?extraheader\s*=\s*)[^\n]*`)
)

// Shown is text from git as the app shows or keeps it: control
// characters stripped and secrets hidden.
func Shown(s string) string { return Secrets(Control(s)) }

// Hidden replaces what Secrets removes.
const Hidden = "<redacted>"

// Secrets hides credentials before text goes into the command log, an
// error report or the crash log: the user part of URLs, auth and cookie
// headers, http.extraHeader values, the password and token lines of git's
// credential protocol, and tokens of well-known services by their shape.
func Secrets(s string) string {
	s = userinfoRE.ReplaceAllString(s, "${1}"+Hidden+"@")
	s = headerRE.ReplaceAllString(s, "${1}${2}"+Hidden)
	s = extraRE.ReplaceAllString(s, "${1}"+Hidden)
	s = pairRE.ReplaceAllString(s, "${1}"+Hidden)
	return tokenRE.ReplaceAllString(s, Hidden)
}

// Host returns the host a remote URL connects to, for the askpass window,
// so "https://github.com@evil.example/x" shows evil.example. It reads
// git's scp-like form, user@host:path, too. ok is false when there is no
// host to show, as for a local path.
func Host(remote string) (host string, ok bool) {
	if i := strings.Index(remote, "://"); i > 0 {
		u, err := url.Parse(remote)
		if err != nil || u.Hostname() == "" {
			return "", false
		}
		return u.Hostname(), true
	}
	// scp-like: [user@]host:path, where the host has no slash before the
	// colon.
	colon := strings.IndexByte(remote, ':')
	if colon <= 0 || strings.ContainsAny(remote[:colon], "/\\") || colon == 1 {
		// No host, a path, or a drive letter as git reads C:\repo.
		return "", false
	}
	h := remote[:colon]
	if at := strings.LastIndexByte(h, '@'); at >= 0 {
		h = h[at+1:]
	}
	h = strings.Trim(h, "[]")
	return h, h != ""
}

// ErrBadURL is returned for a remote URL the app will not store.
var ErrBadURL = errors.New("redact: remote URL has control characters or starts with a dash")

// CheckRemoteURL rejects a URL for a new or changed remote that holds
// control characters (which can split a config line or a credential
// request) or starts with a dash (which git would take as an option).
func CheckRemoteURL(u string) error {
	if u == "" || strings.HasPrefix(u, "-") {
		return ErrBadURL
	}
	for _, r := range u {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || r == 0x2028 || r == 0x2029 {
			return ErrBadURL
		}
	}
	return nil
}
