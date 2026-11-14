package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// GitHub's stacked pull requests, through the gh stack extension
// (github/gh-stack) when the user has it. Its stacks are read from the
// file it keeps in the git directory, which needs no network; what
// changes them runs gh stack.

// Stack is a stack gh stack keeps: branches from the bottom, nearest the
// trunk, to the top, each with its pull request once it has one.
type Stack struct {
	// Number is GitHub's number for it, 0 before it is on GitHub.
	Number   int
	Trunk    string
	Branches []StackBranch
}

// StackBranch is a branch of a stack, by its short name.
type StackBranch struct {
	Name   string
	PR     int
	URL    string
	Merged bool
}

// stackFile bounds the file read; a stack is a few hundred bytes.
const stackFile = 4 << 20

// ReadStacks reads the stacks gh stack keeps for the worktree whose git
// directory is gitDir: none when it keeps no file there.
func ReadStacks(gitDir string) ([]Stack, error) {
	f, err := os.Open(filepath.Join(gitDir, "gh-stack"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, stackFile+1))
	if err != nil {
		return nil, err
	}
	if len(b) > stackFile {
		return nil, fmt.Errorf("forge: %s is larger than %d bytes", f.Name(), stackFile)
	}
	return parseStacks(b)
}

type stackRef struct {
	Branch string `json:"branch"`
	PR     *struct {
		Number int    `json:"number"`
		URL    string `json:"url"`
		Merged bool   `json:"merged"`
	} `json:"pullRequest"`
}

func parseStacks(b []byte) ([]Stack, error) {
	var file struct {
		SchemaVersion int `json:"schemaVersion"`
		Stacks        []struct {
			Number   int        `json:"number"`
			Trunk    stackRef   `json:"trunk"`
			Branches []stackRef `json:"branches"`
		} `json:"stacks"`
	}
	if err := json.Unmarshal(b, &file); err != nil {
		return nil, fmt.Errorf("forge: gh stack's file: %w", err)
	}
	var out []Stack
	for _, s := range file.Stacks {
		st := Stack{Number: s.Number, Trunk: s.Trunk.Branch}
		for _, br := range s.Branches {
			sb := StackBranch{Name: br.Branch}
			if br.PR != nil {
				sb.PR, sb.URL, sb.Merged = br.PR.Number, br.PR.URL, br.PR.Merged
			}
			st.Branches = append(st.Branches, sb)
		}
		if len(st.Branches) > 0 {
			out = append(out, st)
		}
	}
	return out, nil
}

// StackOf is the stack branch (a short name) is in, and where, bottom
// first.
func StackOf(stacks []Stack, branch string) (Stack, int, bool) {
	for _, s := range stacks {
		for i, b := range s.Branches {
			if b.Name == branch {
				return s, i, true
			}
		}
	}
	return Stack{}, 0, false
}

// HasStack reports whether gh has the stack extension, and its version.
func (c *CLI) HasStack(ctx context.Context) (string, bool) {
	out, err := c.run(ctx, "", "", "stack", "--version")
	if err != nil {
		return "", false
	}
	v, ok := strings.CutPrefix(strings.TrimSpace(string(out)), "gh stack version ")
	return v, ok
}

// StackTimeout bounds a gh stack write: it pushes every branch.
const StackTimeout = 10 * time.Minute

// Stack runs gh stack with args in the work tree dir, its output to
// stdout and stderr as it comes. git asks nothing on a terminal: there
// is none.
func (c *CLI) Stack(ctx context.Context, dir string, stdout, stderr io.Writer, args ...string) error {
	select {
	case c.slots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-c.slots }()
	ctx, cancel := context.WithTimeout(ctx, StackTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Path, append([]string{"stack"}, args...)...)
	said := &capped{max: 64 << 10}
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, stdout, io.MultiWriter(stderr, said)
	var env []string
	for _, kv := range c.env {
		// A forced terminal would have it wait on prompts no one sees.
		if !strings.HasPrefix(kv, "GH_FORCE_TTY=") {
			env = append(env, kv)
		}
	}
	cmd.Env = append(env, "GIT_TERMINAL_PROMPT=0")
	cmd.WaitDelay = 2 * time.Second
	ownGroup(cmd)
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("forge: gh stack did not finish within %v", StackTimeout)
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if msg := lastFailure(said.b.String()); msg != "" {
			return fmt.Errorf("gh stack: %s", msg)
		}
	}
	return err
}

// lastFailure is gh stack's last message of failure ("✗ ..."), or its
// last line.
func lastFailure(stderr string) string {
	var last string
	for _, l := range strings.Split(stderr, "\n") {
		l = strings.TrimSpace(l)
		if m, ok := strings.CutPrefix(l, "✗ "); ok {
			return m
		}
		if l != "" {
			last = l
		}
	}
	return last
}
