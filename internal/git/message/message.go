// Package message reads what a commit message starts from: the user's
// commit.template, the messages of recent commits, the message of the
// commit being amended; and checks one before it is written.
package message

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"ramus/internal/git/run"
)

// MaxTemplate bounds a commit template read.
const MaxTemplate = 64 << 10

// Where names the repository.
type Where struct {
	GitDir, WorkTree string
}

func (w Where) git(ctx context.Context, r *run.Runner, args ...string) (string, error) {
	res, err := r.Run(ctx, &run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Read: true, Args: args})
	return string(res.Stdout), err
}

// Template returns the commit.template file's text, "" when none is set.
// git reads a relative path from where it runs, the top of the work tree.
func Template(ctx context.Context, r *run.Runner, w Where) (string, error) {
	out, err := w.git(ctx, r, "config", "--path", "--get", "commit.template")
	var e *run.Error
	if errors.As(err, &e) && e.ExitCode == 1 {
		return "", nil // not set
	}
	if err != nil {
		return "", err
	}
	p := strings.TrimSuffix(out, "\n")
	if !filepath.IsAbs(p) {
		p = filepath.Join(w.WorkTree, p)
	}
	f, err := os.Open(p)
	if err != nil {
		return "", fmt.Errorf("message: commit.template: %w", err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxTemplate))
	return string(b), err
}

// CommentChar returns core.commentChar, "#" unless set; "auto" is read
// as "#", the character git falls back to.
func CommentChar(ctx context.Context, r *run.Runner, w Where) string {
	out, err := w.git(ctx, r, "config", "--get", "core.commentChar")
	c := strings.TrimSuffix(out, "\n")
	if err != nil || c == "" || c == "auto" {
		return "#"
	}
	return c
}

// StripTemplate removes from msg the template's comment lines the user
// left in, and nothing else: a line of their own that starts with the
// comment character, an issue number say, stays.
func StripTemplate(msg, template, comment string) string {
	drop := map[string]bool{}
	for _, l := range strings.Split(template, "\n") {
		if strings.HasPrefix(l, comment) {
			drop[strings.TrimRight(l, " \t\r")] = true
		}
	}
	if len(drop) == 0 {
		return msg
	}
	var keep []string
	for _, l := range strings.Split(msg, "\n") {
		if !drop[strings.TrimRight(l, " \t\r")] {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "\n")
}

// Recent returns the messages of the last n commits on HEAD, newest
// first, each once; none for a branch with no commits.
func Recent(ctx context.Context, r *run.Runner, w Where, n int) ([]string, error) {
	var e *run.Error
	if _, err := w.git(ctx, r, "rev-parse", "-q", "--verify", "HEAD"); errors.As(err, &e) && e.ExitCode == 1 {
		return nil, nil // no commit yet
	}
	out, err := w.git(ctx, r, "log", fmt.Sprintf("-n%d", n), "--format=%B%x00", "HEAD", "--")
	if err != nil {
		return nil, err
	}
	var msgs []string
	seen := map[string]bool{}
	for _, m := range strings.Split(out, "\x00") {
		m = strings.TrimSpace(m)
		if m != "" && !seen[m] {
			seen[m] = true
			msgs = append(msgs, m)
		}
	}
	return msgs, nil
}

// Last returns HEAD's message, for amending it.
func Last(ctx context.Context, r *run.Runner, w Where) (string, error) {
	out, err := w.git(ctx, r, "log", "-1", "--format=%B", "HEAD", "--")
	return strings.TrimRight(out, "\n"), err
}

// ErrEmpty is a message with no text.
var ErrEmpty = errors.New("message: the commit message is empty")

// Check refuses a message git would refuse or mangle: empty, or holding
// a NUL.
func Check(msg string) error {
	if strings.TrimSpace(msg) == "" {
		return ErrEmpty
	}
	if strings.IndexByte(msg, 0) >= 0 {
		return errors.New("message: the commit message holds a NUL character")
	}
	return nil
}

var personRE = regexp.MustCompile(`^[^<>\n]+ <[^<>\s]+>$`)

// CheckPerson checks a co-author as a trailer takes one: "Name <email>".
func CheckPerson(p string) error {
	if !personRE.MatchString(strings.TrimSpace(p)) {
		return fmt.Errorf("message: %q is not in the form Name <email>", p)
	}
	return nil
}

// Join puts a subject and a body together as git expects: a blank line
// between them.
func Join(subject, body string) string {
	subject = strings.TrimSpace(subject)
	body = strings.Trim(body, "\n")
	if strings.TrimSpace(body) == "" {
		return subject
	}
	return subject + "\n\n" + body
}

// Split is the reverse of Join, for a message loaded into the editor.
func Split(msg string) (subject, body string) {
	subject, body, _ = strings.Cut(strings.TrimLeft(msg, "\n"), "\n")
	return subject, string(bytes.TrimLeft([]byte(body), "\n"))
}
