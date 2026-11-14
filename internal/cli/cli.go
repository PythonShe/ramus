// Package cli reads what the app was asked to do from its command line:
// open a repository, compare two files or folders, show a file's log or
// blame, or resolve a merge as git mergetool. Paths are made absolute
// against the directory the command ran in, so the request still means
// the same once it reaches a running instance.
package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Kind is what a request asks for.
type Kind uint8

const (
	None  Kind = iota // just start
	Open              // a repository
	Diff              // two files or folders
	Log               // a file's history
	Blame             // a file's lines by commit
	Merge             // a conflicted file, as git mergetool
)

// Request is one parsed command line.
type Request struct {
	Kind Kind
	// Paths, absolute: Open one, Diff two (old, new), Log and Blame one,
	// Merge four (base or "", local, remote, merged).
	Paths []string
	// Wait keeps the command running until the user is done, as git
	// difftool and mergetool expect; its exit code says whether a merge
	// was resolved.
	Wait bool
}

// Usage is printed for a command line that does not parse.
const Usage = `usage:
  ramus [<repository>]
  ramus diff [--wait] <old> <new>
  ramus log <file>
  ramus blame <file>
  ramus merge [--wait] [<base>] <local> <remote> <merged>`

// ErrUsage wraps every parse error.
var ErrUsage = errors.New("cli: bad command line")

// Parse reads args (without the program name) run in directory cwd.
func Parse(args []string, cwd string) (Request, error) {
	abs := func(p string) string {
		switch {
		case p == "" || filepath.IsAbs(p):
			return filepath.Clean(p)
		case filepath.VolumeName(p) == "" && os.IsPathSeparator(p[0]):
			// On Windows, rooted on the drive of the folder it was typed in.
			return filepath.Join(filepath.VolumeName(cwd), p)
		}
		return filepath.Join(cwd, p)
	}
	bad := func(format string, a ...any) (Request, error) {
		return Request{}, fmt.Errorf("%w: %s", ErrUsage, fmt.Sprintf(format, a...))
	}
	if len(args) == 0 {
		return Request{}, nil
	}
	var r Request
	var rest []string
	sub := args[0]
	switch sub {
	case "diff":
		r.Kind = Diff
	case "log":
		r.Kind = Log
	case "blame":
		r.Kind = Blame
	case "merge":
		r.Kind = Merge
	default:
		r.Kind = Open
		rest = args
	}
	if r.Kind != Open {
		dashdash := false
		for _, a := range args[1:] {
			switch {
			case dashdash || !strings.HasPrefix(a, "-") || a == "-":
				rest = append(rest, a)
			case a == "--":
				dashdash = true
			case a == "--wait" && (r.Kind == Diff || r.Kind == Merge):
				r.Wait = true
			default:
				return bad("unknown option %s", a)
			}
		}
	} else if len(rest) == 1 && strings.HasPrefix(rest[0], "-") {
		return bad("unknown option %s", rest[0])
	}
	want := map[Kind][]int{Open: {1}, Diff: {2}, Log: {1}, Blame: {1}, Merge: {3, 4}}[r.Kind]
	if !contains(want, len(rest)) {
		return bad("%s takes %s, not %d", name(r.Kind), counts(want), len(rest))
	}
	if r.Kind == Merge && len(rest) == 3 {
		rest = append([]string{""}, rest...)
	}
	for _, p := range rest {
		r.Paths = append(r.Paths, abs(p))
	}
	if r.Kind == Merge && r.Paths[0] == "." {
		r.Paths[0] = ""
	}
	return r, nil
}

func contains(ns []int, n int) bool {
	for _, x := range ns {
		if x == n {
			return true
		}
	}
	return false
}

func counts(ns []int) string {
	if len(ns) == 2 {
		return fmt.Sprintf("%d or %d paths", ns[0], ns[1])
	}
	if ns[0] == 1 {
		return "one path"
	}
	return fmt.Sprintf("%d paths", ns[0])
}

func name(k Kind) string {
	return map[Kind]string{Open: "ramus", Diff: "diff", Log: "log", Blame: "blame", Merge: "merge"}[k]
}

// Args turns a request back into arguments, for handing it to a running
// instance.
func (r Request) Args() []string {
	switch r.Kind {
	case None:
		return nil
	case Open:
		return append([]string(nil), r.Paths...)
	}
	a := []string{name(r.Kind)}
	if r.Wait {
		a = append(a, "--wait")
	}
	a = append(a, "--")
	for i, p := range r.Paths {
		if r.Kind == Merge && i == 0 && p == "" {
			continue
		}
		a = append(a, p)
	}
	return a
}

// ToolConfig is the git config that makes the app git's difftool and
// mergetool, for the user to copy; the app never writes it.
func ToolConfig(program string) string {
	q := quote(program)
	return `[diff]
	tool = ramus
[difftool "ramus"]
	cmd = ` + q + ` diff --wait "$LOCAL" "$REMOTE"
[merge]
	tool = ramus
[mergetool "ramus"]
	cmd = ` + q + ` merge --wait "$BASE" "$LOCAL" "$REMOTE" "$MERGED"
	trustExitCode = true
`
}

// quote writes program for git's config and the shell git runs cmd in.
func quote(p string) string {
	if !strings.ContainsAny(p, " \t'\"\\$`") {
		return p
	}
	return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
}
