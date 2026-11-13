package session

import (
	"bytes"
	"context"
	"strconv"
	"strings"

	"ramus/internal/git/run"
)

// MaxClean bounds the paths a clean lists.
const MaxClean = 10000

// ListClean asks git clean what it would delete, untracked folders
// included and ignored files too when ignored is set, off the main thread,
// and hands the paths to done on it. A folder ends in "/".
func (s *Session) ListClean(ignored bool, done func([]string, error)) {
	r := s.Repo
	if r == nil {
		return
	}
	args := []string{"clean", "-n", "-d"}
	if ignored {
		args = append(args, "-x")
	}
	update := s.update
	r.Life().Go(func(ctx context.Context) {
		res, err := r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, WorkTree: r.WorkTree, Read: true, MaxOutput: 16 << 20, Args: args})
		var paths []string
		if err == nil {
			paths = parseClean(res.Stdout)
		}
		update(func() { done(paths, err) })
	})
}

// parseClean reads git clean -n: "Would remove <path>" a line, the path
// C-quoted when it holds anything unusual.
func parseClean(out []byte) []string {
	var paths []string
	for line := range bytes.SplitSeq(out, []byte{'\n'}) {
		p, ok := strings.CutPrefix(string(line), "Would remove ")
		if !ok || p == "" {
			continue
		}
		if p[0] == '"' {
			// git's C quoting escapes as Go's does: \" \\ \n \t and octal.
			u, err := strconv.Unquote(p)
			if err != nil {
				continue
			}
			p = u
		}
		paths = append(paths, p)
		if len(paths) == MaxClean {
			break
		}
	}
	return paths
}
