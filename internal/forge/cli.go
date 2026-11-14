package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Bounds on the tool: how long one call may take, how much it may print,
// how many run at once, and how many branches one call asks about.
const (
	Timeout     = 30 * time.Second
	maxOutput   = 8 << 20
	maxRunning  = 2
	branchBatch = 50
	// MaxBranches bounds the branches asked about in one repository.
	MaxBranches = 500
)

// CLI is the user's own gh or glab, run with their environment.
type CLI struct {
	Kind Kind
	Path string
	env  []string
	// slots bounds the calls running at once.
	slots chan struct{}
}

// ErrNoCLI is a host whose tool is not installed.
var ErrNoCLI = errors.New("forge: the host's command-line tool is not installed")

// LoginError is a tool not logged in to the host, or whose login expired.
type LoginError struct {
	Program, Host string
}

func (e *LoginError) Error() string {
	return fmt.Sprintf("%s is not logged in to %s: run %s auth login in a terminal", e.Program, e.Host, e.Program)
}

// Program is the tool a kind of host is asked through.
func (k Kind) Program() string {
	switch k {
	case GitHub:
		return "gh"
	case GitLab:
		return "glab"
	}
	return ""
}

// Find looks for the tool of kind on env's PATH.
func Find(env []string, k Kind) (*CLI, error) {
	name := k.Program()
	if name == "" {
		return nil, ErrNoCLI
	}
	p, ok := lookPath(env, name)
	if !ok {
		return nil, ErrNoCLI
	}
	return &CLI{Kind: k, Path: p, env: quiet(env), slots: make(chan struct{}, maxRunning)}, nil
}

// lookPath finds an executable named name in env's PATH, not the app's,
// which the desktop starts with only the system's folders.
func lookPath(env []string, name string) (string, bool) {
	path := ""
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.EqualFold(k, "PATH") {
			path = v
		}
	}
	names := []string{name}
	if runtime.GOOS == "windows" {
		names = []string{name + ".exe"}
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		for _, n := range names {
			p := filepath.Join(dir, n)
			fi, err := os.Stat(p)
			if err == nil && fi.Mode().IsRegular() && (runtime.GOOS == "windows" || fi.Mode()&0o111 != 0) {
				return p, true
			}
		}
	}
	return "", false
}

// quiet is env with the tools asking nothing and printing no notices:
// no one answers them.
func quiet(env []string) []string {
	out := make([]string, 0, len(env)+8)
	out = append(out, env...)
	return append(out,
		"GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1", "GH_NO_EXTENSION_UPDATE_NOTIFIER=1",
		"GH_SPINNER_DISABLED=1", "NO_COLOR=1", "CLICOLOR=0",
		"NO_PROMPT=1", "GLAB_CHECK_UPDATE=false", "GLAB_SEND_TELEMETRY=false",
	)
}

// run runs the tool with args in dir, which may be "", and returns what
// it printed.
func (c *CLI) run(ctx context.Context, dir string, host string, args ...string) ([]byte, error) {
	select {
	case c.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-c.slots }()
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Path, args...)
	cmd.Dir, cmd.Env = dir, c.env
	cmd.WaitDelay = 2 * time.Second
	ownGroup(cmd)
	out := &capped{max: maxOutput}
	var stderr capped
	stderr.max = 64 << 10
	cmd.Stdout, cmd.Stderr = out, &stderr
	err := cmd.Run()
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return nil, fmt.Errorf("forge: %s did not answer within %v", c.Kind.Program(), Timeout)
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case out.over:
		return nil, fmt.Errorf("forge: %s printed more than %d bytes", c.Kind.Program(), maxOutput)
	case err != nil:
		return nil, c.failure(err, host, stderr.b.String())
	}
	return out.b.Bytes(), nil
}

// failure says why the tool failed: a login missing, or its message.
func (c *CLI) failure(err error, host, stderr string) error {
	var ee *exec.ExitError
	code := -1
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	}
	low := strings.ToLower(stderr)
	// gh exits 4 when it needs a login; both tools then name the command.
	if (c.Kind == GitHub && code == 4) || strings.Contains(low, "auth login") || strings.Contains(low, "401 unauthorized") {
		return &LoginError{Program: c.Kind.Program(), Host: host}
	}
	msg := strings.TrimSpace(stderr)
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	if msg == "" {
		return fmt.Errorf("forge: %s: %w", c.Kind.Program(), err)
	}
	return fmt.Errorf("%s: %s", c.Kind.Program(), msg)
}

// capped keeps what is written up to max bytes, and notes more.
type capped struct {
	b    bytes.Buffer
	max  int
	over bool
}

func (w *capped) Write(p []byte) (int, error) {
	if room := w.max - w.b.Len(); len(p) > room {
		w.b.Write(p[:max(room, 0)])
		w.over = true
		return len(p), nil
	}
	return w.b.Write(p)
}

// Pulls asks the host for the open pull requests from branches, as the
// remote names them, made in r itself; a branch with none is left out.
// At most MaxBranches are asked about.
func (c *CLI) Pulls(ctx context.Context, r Repo, branches []string) (map[string]Pull, error) {
	if r.Kind != c.Kind {
		return nil, ErrNoCLI
	}
	branches = branches[:min(len(branches), MaxBranches)]
	out := map[string]Pull{}
	for len(branches) > 0 {
		batch := branches[:min(len(branches), branchBatch)]
		branches = branches[len(batch):]
		var err error
		switch r.Kind {
		case GitHub:
			err = c.githubPulls(ctx, r, batch, out)
		case GitLab:
			err = c.gitlabPulls(ctx, r, batch, out)
		}
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// graphQLError is the errors a GraphQL answer carries.
type graphQLError struct {
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func (g graphQLError) err(program string) error {
	if len(g.Errors) == 0 {
		return nil
	}
	return fmt.Errorf("%s: %s", program, g.Errors[0].Message)
}

// literal writes s as a GraphQL string: JSON's escapes are GraphQL's.
func literal(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
