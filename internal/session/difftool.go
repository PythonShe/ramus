package session

import (
	"cmp"
	"context"
	"encoding/hex"
	"errors"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	"ramus/internal/git/run"
	"ramus/internal/git/write"
	"ramus/locales"
)

// DiffToolRequest names what an external diff tool compares: paths of the
// work tree against the index, of the index against HEAD (Cached), or
// between commits From and To when they are set, or of the work tree
// against From when only it is.
type DiffToolRequest struct {
	Paths    []string
	Cached   bool
	From, To []byte
	// Own is the command git runs for the app as its difftool
	// (cli.ToolCommands), run when the config names no tool; "" leaves
	// ErrNoDiffTool.
	Own string
}

// ownTool is the name the app's own difftool is given for one run.
const ownTool = "ramus-own"

// maxDiffTools bounds the external diff tools open at once for one
// repository; diffToolTime bounds how long one stays open.
const (
	maxDiffTools = 4
	diffToolTime = 4 * time.Hour
)

// ErrNoDiffTool is a repository whose git config names no diff tool, nor
// a merge tool for git difftool to fall back to.
var ErrNoDiffTool error = &sayError{locales.SessionNoDiffTool}

// diffTools counts the tools open, which run in the session's lifetime,
// not the repository's: Trust closing the repository to open it again
// leaves the user's tool, with what they edited in it, open. Closing the
// session (the tab) stops them, as it did before.
type diffTools struct {
	open atomic.Int32
	// ctx is cancelled as the session closes; made with the first tool.
	// Both are used on the main thread only.
	ctx    context.Context
	cancel context.CancelFunc
}

// lifetime is the context the tools run in, which close cancels.
func (d *diffTools) lifetime() context.Context {
	if d.ctx == nil {
		d.ctx, d.cancel = context.WithCancel(context.Background())
	}
	return d.ctx
}

// close stops the tools open.
func (d *diffTools) close() {
	if d.cancel != nil {
		d.cancel()
	}
}

// OpenDiffTool hands a comparison to the diff tool git's config names,
// or else to req.Own, through git difftool, and calls done on the main thread once the tool
// is closed or did not start. Only a trusted repository runs it: the tool
// is a command from the config.
func (s *Session) OpenDiffTool(req DiffToolRequest, done func(error)) {
	r := s.Repo
	switch {
	case r == nil:
		done(errors.New("session: the repository is not open"))
		return
	case r.Restricted:
		done(run.ErrRestricted)
		return
	}
	// git difftool falls back to the merge tool when no diff tool is set.
	cfg := r.Config()
	get := func(keys ...string) string {
		for _, k := range keys {
			if v, _ := cfg.Get(k); v != "" {
				return v
			}
		}
		return ""
	}
	gui, tool := get("diff.guitool", "merge.guitool"), get("diff.tool", "merge.tool")
	var config []string
	if gui == "" && tool == "" {
		if req.Own == "" {
			done(ErrNoDiffTool)
			return
		}
		// The app's own compare window, for this run only: git's config
		// is the user's, and stays as it is.
		config, tool = []string{"difftool." + ownTool + ".cmd=" + req.Own}, ownTool
	}
	// git runs a terminal tool, as vimdiff, with no terminal to show it
	// in: it would wait unseen for its time, holding a place of the
	// few open at once. git difftool reads a tool's command from
	// difftool.<tool>.cmd, then mergetool.<tool>.cmd.
	if name := cmp.Or(gui, tool); name != ownTool && write.TerminalTool(name, get("difftool."+name+".cmd", "mergetool."+name+".cmd")) {
		done(errors.New(locales.SessionDiffTerminalTool(name)))
		return
	}
	if s.tools.open.Add(1) > maxDiffTools {
		s.tools.open.Add(-1)
		done(errors.New(locales.SessionTooManyDiffTools()))
		return
	}
	args := []string{"difftool", "--no-prompt"}
	switch {
	case tool == ownTool:
		args = append(args, "--tool="+ownTool)
	case gui != "":
		args = append(args, "--gui")
	}
	switch {
	case req.From != nil && req.To != nil:
		args = append(args, hex.EncodeToString(req.From), hex.EncodeToString(req.To))
	case req.From != nil:
		args = append(args, hex.EncodeToString(req.From))
	case req.Cached:
		args = append(args, "--cached")
	}
	base := run.Cmd{GitDir: r.GitDir, WorkTree: r.WorkTree, Args: append(args, "--"), Config: config, NoPrompt: true}
	// Literal by magic, not by GIT_LITERAL_PATHSPECS: the tool, and every
	// terminal and git it starts, would keep that.
	paths := make([]string, len(req.Paths))
	for i, p := range req.Paths {
		paths[i] = ":(literal)" + p
	}
	// git difftool reads paths only as arguments and opens the files one
	// at a time: many take several runs, one after another.
	runs := write.SplitArgs(base, paths)
	if len(runs) == 0 {
		runs = []run.Cmd{base}
	}
	life := s.tools.lifetime()
	// Done waits for it. While a repository is open, running counts it
	// already, so this Add comes before any Wait.
	s.running.Add(1)
	go func() {
		defer s.running.Done()
		defer s.recoverWork()
		defer s.tools.open.Add(-1)
		ctx, cancel := context.WithTimeout(life, diffToolTime)
		defer cancel()
		var stderr strings.Builder
		var err error
		// The tool may outlive the app and start terminals of its own:
		// it gets the user's environment, without the app's askpass,
		// which difftool --no-prompt never needs, nor what the runner
		// sets for its own commands.
		own := len(r.Runner.Environ())
		for _, c := range runs {
			cmd := r.Runner.Command(ctx, &c)
			env := cmd.Env[:own:own]
			for _, e := range cmd.Env[own:] {
				if !strings.HasPrefix(e, "GIT_TERMINAL_PROMPT=") && !strings.HasPrefix(e, "GIT_LFS_FORCE_PROGRESS=") {
					env = append(env, e)
				}
			}
			cmd.Env = env
			cmd.Stderr = &limitedWriter{w: &stderr, left: 8 << 10}
			untrack := r.Runner.Track()
			err = r.Runner.Start(cmd)
			if err == nil {
				err = cmd.Wait()
				if errors.Is(err, exec.ErrWaitDelay) && ctx.Err() == nil {
					// git exited 0 (Wait says ErrWaitDelay only then); what
					// still holds its stderr is a process the tool started
					// and left running, as one handing the files to a window
					// already open does.
					err = nil
				}
			}
			untrack()
			if err != nil {
				break
			}
		}
		if err != nil && ctx.Err() == nil {
			if msg := strings.TrimSpace(stderr.String()); msg != "" {
				err = errors.New(msg)
			}
		}
		if ctx.Err() != nil && life.Err() != nil {
			return // the session closed, which ended the tool
		}
		// The tool's own end is told whichever repository is open now, as
		// after Trust opened it again: what it says is of the tool and
		// holds nothing of r.
		s.update(func() {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if !closed {
				done(err)
			}
		})
	}()
}

// limitedWriter keeps the first bytes written and drops the rest.
type limitedWriter struct {
	w    *strings.Builder
	left int
}

func (l *limitedWriter) Write(b []byte) (int, error) {
	if l.left > 0 {
		n := min(len(b), l.left)
		l.w.Write(b[:n])
		l.left -= n
	}
	return len(b), nil
}
