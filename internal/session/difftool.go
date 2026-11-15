package session

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"sync/atomic"
	"time"

	"ramus/internal/git/run"
	"ramus/locales"
)

// DiffToolRequest names what an external diff tool compares: paths of the
// work tree against the index, of the index against HEAD (Cached), or
// between commits From and To when they are set.
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

// diffTools counts the tools open.
type diffTools struct{ open atomic.Int32 }

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
	case req.Cached:
		args = append(args, "--cached")
	}
	args = append(append(args, "--"), req.Paths...)
	update := s.update
	r.Life().Go(func(ctx context.Context) {
		defer s.tools.open.Add(-1)
		ctx, cancel := context.WithTimeout(ctx, diffToolTime)
		defer cancel()
		cmd := r.Runner.Command(ctx, &run.Cmd{GitDir: r.GitDir, WorkTree: r.WorkTree, Args: args, Config: config,
			Env: []string{"GIT_LITERAL_PATHSPECS=1"}})
		var stderr strings.Builder
		cmd.Stderr = &limitedWriter{w: &stderr, left: 8 << 10}
		untrack := r.Runner.Track()
		err := r.Runner.Start(cmd)
		if err == nil {
			err = cmd.Wait()
		}
		untrack()
		if err != nil && ctx.Err() == nil {
			if msg := strings.TrimSpace(stderr.String()); msg != "" {
				err = errors.New(msg)
			}
		}
		if ctx.Err() != nil && r.Life().Context().Err() != nil {
			return // the repository closed
		}
		update(func() { done(err) })
	})
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
