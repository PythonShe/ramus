package session

import (
	"bytes"
	"context"
	"strings"

	"ramus/internal/git/pathsafe"
	"ramus/internal/git/run"
	"ramus/internal/git/write"
)

// MaxClean bounds the paths a clean lists.
const MaxClean = 10000

// ListClean asks git clean what it would delete, untracked folders
// included and ignored files too when ignored is set, off the main thread,
// and hands the paths to done on it, with their state then for the clean
// to check. A folder ends in "/". A listing whose repository closed and
// opened again (Trust) is answered ErrReopened.
func (s *Session) ListClean(ignored bool, done func([]string, []write.FileState, error)) {
	r := s.Repo
	if r == nil {
		done(nil, nil, errNotOpen)
		return
	}
	args := []string{"clean", "-n", "-d"}
	if ignored {
		args = append(args, "-x")
	}
	r.Life().Go(func(ctx context.Context) {
		// git clean has no -z; it is read in English, its paths quoted only
		// where they must be.
		res, err := r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, WorkTree: r.WorkTree, Read: true, MaxOutput: 16 << 20, Args: args,
			Untranslated: true, Config: []string{"core.quotePath=false"}})
		var paths []string
		var seen []write.FileState
		if err == nil {
			paths = parseClean(res.Stdout)
			if ignored {
				seen, err = write.Observe(r.WorkTree, paths)
			} else {
				// A clean without -x leaves what git ignores alone.
				seen, err = write.ObserveIgnoring(ctx, r.Runner, write.Where{GitDir: r.GitDir, WorkTree: r.WorkTree}, paths)
			}
		}
		s.handOver(r, func() { done(paths, seen, err) }, func() { done(nil, nil, ErrReopened) })
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
		paths = append(paths, pathsafe.Unquote(p))
		if len(paths) == MaxClean {
			break
		}
	}
	return paths
}
