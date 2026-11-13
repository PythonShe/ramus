package session

import (
	"bytes"
	"context"
	"slices"
	"strconv"
	"time"

	"ramus/internal/git/repo"
	"ramus/internal/git/run"
)

// PathWeights are the files of a treemap, each with a weight: its size,
// or how many commits changed it.
type PathWeights struct {
	// Paths are sorted and NUL-terminated, back to back, as pathtree.Build
	// takes them; Weights holds one weight a path, in that order.
	Paths   []byte
	Weights []int64
	// Commits is the commits read for change counts; Limited is set when
	// they were cut at MaxChangeCommits.
	Commits int
	Limited bool
	Err     error
}

// MaxChangeCommits bounds the commits read for change counts when no
// start date bounds them: the latest ones are read.
const MaxChangeCommits = 20000

// treemapTimeout bounds reading a history's changed paths.
const treemapTimeout = 15 * time.Minute

// FileSizes reads the size of every file in the tree of rev (a full ref
// name, or HEAD when empty) off the main thread, and hands them to done
// on it.
func (s *Session) FileSizes(rev string, done func(PathWeights)) {
	r := s.Repo
	if r == nil {
		return
	}
	if rev == "" {
		rev = "HEAD"
	}
	update := s.update
	r.Life().Go(func(ctx context.Context) {
		pw := fileSizes(ctx, r, rev)
		update(func() { done(pw) })
	})
}

func fileSizes(ctx context.Context, r *repo.Repository, rev string) PathWeights {
	res, err := r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, Read: true, MaxOutput: 1 << 30,
		Args: []string{"ls-tree", "-r", "-l", "-z", "--full-tree", "--end-of-options", rev}})
	if err != nil {
		return PathWeights{Err: err}
	}
	var pw PathWeights
	for entry := range bytes.SplitSeq(res.Stdout, []byte{0}) {
		// <mode> SP <type> SP <object> SP+ <size> TAB <path>
		meta, path, ok := bytes.Cut(entry, []byte{'\t'})
		if !ok {
			continue
		}
		f := bytes.Fields(meta)
		if len(f) != 4 || string(f[1]) != "blob" {
			continue
		}
		size, err := strconv.ParseInt(string(f[3]), 10, 64)
		if err != nil {
			continue
		}
		pw.Paths = append(append(pw.Paths, path...), 0)
		pw.Weights = append(pw.Weights, size)
	}
	// ls-tree lists in git's tree order, where "a.b" comes before "a/";
	// pathtree wants plain byte order of whole paths.
	sortWeights(&pw)
	return pw
}

// ChangeCounts counts the commits that changed each path, over the
// commits req names (merges left out, renames not followed), off the
// main thread, and hands them to done on it.
func (s *Session) ChangeCounts(req StatsRequest, done func(PathWeights)) {
	r := s.Repo
	if r == nil {
		return
	}
	update := s.update
	r.Life().Go(func(ctx context.Context) {
		pw := changeCounts(ctx, r, req)
		update(func() { done(pw) })
	})
}

func changeCounts(ctx context.Context, r *repo.Repository, req StatsRequest) PathWeights {
	args := []string{"log", "-z", "--no-merges", "--no-renames", "--name-only", "--format=%x01", "--no-ext-diff", "--no-textconv"}
	if !req.Since.IsZero() {
		args = append(args, "--since="+strconv.FormatInt(req.Since.Unix(), 10))
	} else {
		args = append(args, "--max-count="+strconv.Itoa(MaxChangeCommits))
	}
	if !req.Until.IsZero() {
		// --until is inclusive; the request's end is not.
		args = append(args, "--until="+strconv.FormatInt(req.Until.Unix()-1, 10))
	}
	if req.Branch != "" {
		args = append(args, "--end-of-options", req.Branch)
	} else {
		args = append(args, "--branches", "--remotes", "--tags", "HEAD")
	}
	res, err := r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, Read: true, Timeout: treemapTimeout, MaxOutput: 1 << 30,
		Args: append(args, "--")})
	if err != nil {
		return PathWeights{Err: err}
	}
	counts := map[string]int64{}
	commits := 0
	for tok := range bytes.SplitSeq(res.Stdout, []byte{0}) {
		// A commit starts "\x01\n" before its first path, and the "\n"
		// that ends its file list stays at the start of the next commit.
		for len(tok) > 0 && (tok[0] == '\n' || tok[0] == 1) {
			if tok[0] == 1 {
				commits++
			}
			tok = tok[1:]
		}
		if len(tok) > 0 {
			counts[string(tok)]++
		}
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	pw := PathWeights{Commits: commits, Limited: req.Since.IsZero() && commits >= MaxChangeCommits}
	for _, k := range keys {
		pw.Paths = append(append(pw.Paths, k...), 0)
		pw.Weights = append(pw.Weights, counts[k])
	}
	return pw
}

// sortWeights puts paths and their weights in byte order of the paths.
func sortWeights(pw *PathWeights) {
	type pathAt struct {
		start, end int
		w          int64
	}
	var ps []pathAt
	start := 0
	for i, b := range pw.Paths {
		if b == 0 {
			ps = append(ps, pathAt{start, i, pw.Weights[len(ps)]})
			start = i + 1
		}
	}
	slices.SortFunc(ps, func(a, b pathAt) int { return bytes.Compare(pw.Paths[a.start:a.end], pw.Paths[b.start:b.end]) })
	paths := make([]byte, 0, len(pw.Paths))
	weights := make([]int64, 0, len(ps))
	for _, p := range ps {
		paths = append(append(paths, pw.Paths[p.start:p.end]...), 0)
		weights = append(weights, p.w)
	}
	pw.Paths, pw.Weights = paths, weights
}
