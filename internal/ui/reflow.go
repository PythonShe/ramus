package ui

import "strings"

// reflowBody joins the lines a commit body's paragraphs of prose were
// broken into at git's 72 columns, for the body to wrap at the width it
// is shown at rather than break twice. The breaks that mean something
// stay: around a blank line, an indented line, a list item, a quote, a
// code fence or a trailer, as Signed-off-by. It is for the eye only: a
// copy takes the message as it was written.
func reflowBody(body string) string {
	var b strings.Builder
	b.Grow(len(body))
	// fence is the run of backticks or tildes that opened the code block
	// the lines are in, "" outside one.
	fence, joins := "", false
	for i, l := range strings.Split(body, "\n") {
		run := fenceRun(l)
		fenceLine := run != ""
		switch {
		case fence == "" && fenceLine:
			fence = run
		case fence != "" && fenceLine && run[0] == fence[0] && len(run) >= len(fence) && strings.TrimSpace(l[len(run):]) == "":
			// Only a run as long of the same, with nothing after, closes.
			fence = ""
		default:
			fenceLine = false
		}
		p := fence == "" && !fenceLine && isProse(l)
		if p {
			l = strings.TrimRight(l, " \t\r")
		}
		if i > 0 {
			if joins && p {
				b.WriteByte(' ')
			} else {
				b.WriteByte('\n')
			}
		}
		b.WriteString(l)
		joins = p
	}
	return b.String()
}

// isProse reports whether l is a line of running text, which may join
// the line of running text before it: not blank, indented, a list item,
// a quote or a trailer.
func isProse(l string) bool {
	if strings.TrimSpace(l) == "" || l[0] == ' ' || l[0] == '\t' || l[0] == '>' {
		return false
	}
	return !listItem(l) && !isTrailer(l)
}

// listItem reports whether l starts with a list's marker: "-", "*" or
// "+", or a number and "." or ")", before a space.
func listItem(l string) bool {
	switch l[0] {
	case '-', '*', '+':
		return len(l) == 1 || l[1] == ' ' || l[1] == '\t'
	}
	i := 0
	for i < len(l) && l[i] >= '0' && l[i] <= '9' {
		i++
	}
	if i == 0 || i == len(l) || l[i] != '.' && l[i] != ')' {
		return false
	}
	return i+1 == len(l) || l[i+1] == ' ' || l[i+1] == '\t'
}

// isTrailer reports whether l is a "Key: value" line, as git's trailers
// are: a key of letters, digits and hyphens.
func isTrailer(l string) bool {
	k, _, ok := strings.Cut(l, ": ")
	if !ok || k == "" {
		return false
	}
	for _, r := range k {
		if r != '-' && (r < '0' || r > '9') && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') {
			return false
		}
	}
	return true
}

// fenceRun is the run of three or more backticks or tildes l starts
// with, "" when it starts with none.
func fenceRun(l string) string {
	if len(l) < 3 || l[0] != '`' && l[0] != '~' {
		return ""
	}
	n := 0
	for n < len(l) && l[n] == l[0] {
		n++
	}
	if n < 3 {
		return ""
	}
	return l[:n]
}
