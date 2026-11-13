// Package actions runs the user's own commands: the variables of an
// action filled in argument by argument, the program started without a
// shell, in the repository, bounded in time and output.
package actions

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Vars are what an action runs on; empty fields are not available where
// it was started.
type Vars struct {
	Repo, Branch, Commit, File string
}

var varRE = regexp.MustCompile(`\$\{([a-z]+)(?::([^}]*))?\}`)

// Prompts returns the questions an action asks before it runs, in order,
// each once.
func Prompts(command []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, arg := range command {
		for _, m := range varRE.FindAllStringSubmatch(arg, -1) {
			if m[1] == "prompt" && !seen[m[2]] {
				seen[m[2]] = true
				out = append(out, m[2])
			}
		}
	}
	return out
}

// ErrMissing is an action that names something it was not started on.
var ErrMissing = errors.New("actions: not available here")

// Expand fills in command's variables. Each argument stays one argument,
// whatever the values hold.
func Expand(command []string, v Vars, answers map[string]string) ([]string, error) {
	if len(command) == 0 {
		return nil, errors.New("actions: no command")
	}
	if !filepath.IsAbs(command[0]) {
		return nil, fmt.Errorf("actions: %q: give the program's full path", command[0])
	}
	out := make([]string, len(command))
	var missing error
	for i, arg := range command {
		out[i] = varRE.ReplaceAllStringFunc(arg, func(s string) string {
			m := varRE.FindStringSubmatch(s)
			val, ok := "", true
			switch m[1] {
			case "repo":
				val = v.Repo
			case "branch":
				val = v.Branch
			case "commit":
				val = v.Commit
			case "file":
				val = v.File
			case "prompt":
				val, ok = answers[m[2]]
			default:
				return s // not ours: left as written
			}
			if val == "" && ok && m[1] != "prompt" {
				missing = fmt.Errorf("%w: ${%s}", ErrMissing, m[1])
			}
			if !ok {
				missing = fmt.Errorf("%w: no answer to %q", ErrMissing, m[2])
			}
			return val
		})
	}
	return out, missing
}

// Timeout bounds an action.
const Timeout = 10 * time.Minute

// MaxLine bounds one line of output kept.
const MaxLine = 16 << 10

// Run runs argv in dir, calling line with each line of its output
// (stdout and stderr together) as it comes, until it exits, ctx ends, or
// Timeout passes.
func Run(ctx context.Context, argv []string, dir string, line func(string)) error {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.WaitDelay = 2 * time.Second
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			l := sc.Text()
			if len(l) > MaxLine {
				l = l[:MaxLine] + "…"
			}
			line(strings.TrimRight(l, "\r"))
		}
		io.Copy(io.Discard, pr)
	}()
	err := cmd.Wait()
	pw.Close()
	<-done
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("actions: stopped after %v", Timeout)
	}
	return err
}
