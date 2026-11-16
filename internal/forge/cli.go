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
	// known are the hosts the tool's config says it is logged in to.
	known *knownHosts
	// Timeout, when set, bounds each call instead of the package's.
	Timeout time.Duration
}

// ErrNoCLI is a host whose tool is not installed.
var ErrNoCLI = errors.New("forge: the host's command-line tool is not installed")

// ErrNotFound is the host answering that what was asked for is not
// there, or not to be seen with the login in use.
var ErrNotFound = errors.New("forge: not found")

// ErrTimeout is the host's tool not answering within Timeout.
var ErrTimeout = errors.New("forge: the host's command-line tool did not answer in time")

// RejectedError is the host refusing a request it understood, as GitHub
// does with 422 for a review on lines outside the diff, or a second
// pending review: Msg is what it said.
type RejectedError struct {
	Msg string
}

func (e *RejectedError) Error() string { return e.Msg }

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
	return &CLI{Kind: k, Path: p, env: quiet(env), slots: make(chan struct{}, maxRunning),
		known: &knownHosts{files: configFiles(env, k)}}, nil
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
	return c.runWith(ctx, dir, host, nil, args...)
}

// runInput is run with stdin on the tool's standard input.
func (c *CLI) runInput(ctx context.Context, host string, stdin []byte, args ...string) ([]byte, error) {
	return c.runWith(ctx, "", host, stdin, args...)
}

func (c *CLI) runWith(ctx context.Context, dir string, host string, stdin []byte, args ...string) ([]byte, error) {
	select {
	case c.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-c.slots }()
	limit := Timeout
	if c.Timeout > 0 {
		limit = c.Timeout
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Path, args...)
	cmd.Dir, cmd.Env = dir, c.env
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	cmd.WaitDelay = 2 * time.Second
	ownGroup(cmd)
	out := &capped{max: maxOutput}
	var stderr capped
	stderr.max = 64 << 10
	cmd.Stdout, cmd.Stderr = out, &stderr
	err := cmd.Run()
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return nil, fmt.Errorf("%w: %s, within %v", ErrTimeout, c.Kind.Program(), limit)
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
	all := strings.TrimSpace(stderr)
	msg := all
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	// gh api ends a REST failure with its status. glab may only say it.
	if strings.Contains(low, "(http 404)") || strings.Contains(low, "404 not found") {
		return fmt.Errorf("%w: %s", ErrNotFound, msg)
	}
	if strings.Contains(low, "(http 422)") {
		// Why comes on the lines after the status.
		return &RejectedError{Msg: rejected(all)}
	}
	if msg == "" {
		return fmt.Errorf("forge: %s: %w", c.Kind.Program(), err)
	}
	return fmt.Errorf("%s: %s", c.Kind.Program(), msg)
}

// rejected is the message of a refusal gh printed, on one line: without
// gh's own prefix, the lines joined, at most 500 bytes of it.
func rejected(stderr string) string {
	var parts []string
	for _, l := range strings.Split(stderr, "\n") {
		if l = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "gh:")); l != "" {
			parts = append(parts, l)
		}
	}
	s := strings.Join(parts, " ")
	if len(s) > 500 {
		s = strings.ToValidUTF8(s[:500], "") + "..."
	}
	return s
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

// Pulls asks base for the pull requests from branches of head, as head
// names them: head is base itself, or a fork of it whose pull requests
// go to base. A branch's open pull request wins, else its latest merged
// or closed one; a branch with none is left out. At most MaxBranches are
// asked about. A host the tool does not know (Knows) is not asked:
// ErrUnknownHost. Their URLs are on base's host (PullURL).
func (c *CLI) Pulls(ctx context.Context, base, head Repo, branches []string) (map[string]Pull, error) {
	if base.Kind != c.Kind || head.Kind != c.Kind {
		return nil, ErrNoCLI
	}
	if !c.Knows(base.Host) {
		return nil, ErrUnknownHost
	}
	branches = branches[:min(len(branches), MaxBranches)]
	out := map[string]Pull{}
	for len(branches) > 0 {
		batch := branches[:min(len(branches), branchBatch)]
		branches = branches[len(batch):]
		var err error
		switch base.Kind {
		case GitHub:
			err = c.githubPulls(ctx, base, head.Owner(), batch, out)
		case GitLab:
			err = c.gitlabPulls(ctx, base, head.Path, batch, out)
		}
		if err != nil {
			return nil, err
		}
	}
	for b, p := range out {
		p.URL = base.PullURL(p.URL, p.Number)
		out[b] = p
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

// Parent is the repository r was forked from, on r's host; ok is false
// when r is no fork. A fork's clone need not name it as a remote, and
// its pull requests and the merged ones of its commits are there. A host
// the tool does not know (Knows) is not asked: ErrUnknownHost.
func (c *CLI) Parent(ctx context.Context, r Repo) (p Repo, ok bool, err error) {
	if r.Kind != c.Kind {
		return Repo{}, false, ErrNoCLI
	}
	if !c.Knows(r.Host) {
		return Repo{}, false, ErrUnknownHost
	}
	var path string
	switch r.Kind {
	case GitHub:
		out, err := c.run(ctx, "", r.Host, "api", "repos/"+r.Path, "--hostname", r.Host)
		if err != nil {
			return Repo{}, false, err
		}
		var a struct {
			Parent *struct {
				FullName string `json:"full_name"`
			} `json:"parent"`
		}
		if err := json.Unmarshal(out, &a); err != nil {
			return Repo{}, false, fmt.Errorf("gh: %w", err)
		}
		if a.Parent != nil {
			path = a.Parent.FullName
		}
	case GitLab:
		out, err := c.run(ctx, "", r.Host, "api", "projects/"+projectID(r.Path), "--hostname", r.Host)
		if err != nil {
			return Repo{}, false, err
		}
		var a struct {
			From *struct {
				Path string `json:"path_with_namespace"`
			} `json:"forked_from_project"`
		}
		if err := json.Unmarshal(out, &a); err != nil {
			return Repo{}, false, fmt.Errorf("glab: %w", err)
		}
		if a.From != nil {
			path = a.From.Path
		}
	}
	// The answer goes into the paths of later calls and of pages opened:
	// what a remote could name, no more.
	parts := strings.Split(path, "/")
	if path == "" || r.Kind == GitHub && len(parts) != 2 || len(parts) < 2 {
		return Repo{}, false, nil
	}
	for _, s := range parts {
		if !namePart(s) {
			return Repo{}, false, nil
		}
	}
	return Repo{Kind: r.Kind, Host: r.Host, Path: path}, true, nil
}
