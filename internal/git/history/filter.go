package history

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"time"

	"ramus/internal/git/commits"
	"ramus/internal/git/run"
)

// Filter narrows history. The zero Filter keeps every commit.
type Filter struct {
	// Author and Message match the author's name or email and the
	// message, as text, ignoring case.
	Author, Message string
	// Changed finds commits that add or remove text matching it: the
	// number of its occurrences changes (git log -S), or, with Regexp, a
	// changed line matches it as a regular expression (-G).
	Changed string
	Regexp  bool
	// Paths keeps commits that touch them, matched literally; git uses
	// the commit-graph's changed-path Bloom filters for these.
	Paths []string
	// Since and Until bound the commit time; zero is no bound.
	Since, Until time.Time
	// FirstParent follows only the first parent of merges; NoMerges
	// leaves merges out.
	FirstParent, NoMerges bool
}

// IsZero reports a filter that keeps everything.
func (f Filter) IsZero() bool {
	return f.Author == "" && f.Message == "" && f.Changed == "" && len(f.Paths) == 0 &&
		f.Since.IsZero() && f.Until.IsZero() && !f.FirstParent && !f.NoMerges
}

// KeepsGraph reports whether what the filter keeps still forms a graph:
// git rewrites parents to the commits kept when it limits by path, and
// first-parent and date bounds keep whole runs of history. Matching text
// picks commits out one by one; those show without edges.
func (f Filter) KeepsGraph() bool {
	return f.Author == "" && f.Message == "" && f.Changed == "" && !f.NoMerges
}

func (f Filter) args() []string {
	args := []string{"--fixed-strings", "--regexp-ignore-case"}
	if f.Author != "" {
		args = append(args, "--author="+f.Author)
	}
	if f.Message != "" {
		args = append(args, "--grep="+f.Message)
	}
	if f.Changed != "" {
		if f.Regexp {
			// -G takes an extended regular expression, whatever
			// --fixed-strings says of --grep and --author.
			args = append(args, "-G"+f.Changed)
		} else {
			args = append(args, "-S"+f.Changed)
		}
	}
	if !f.Since.IsZero() {
		args = append(args, "--since=@"+strconv.FormatInt(f.Since.Unix(), 10))
	}
	if !f.Until.IsZero() {
		args = append(args, "--until=@"+strconv.FormatInt(f.Until.Unix(), 10))
	}
	if f.FirstParent {
		args = append(args, "--first-parent")
	}
	if f.NoMerges {
		args = append(args, "--no-merges")
	}
	return args
}

// Filtered streams the commits f keeps, reachable from tips, through git
// log: numbered in s and handed to fn with their parents, children first,
// as RevList does. When f does not keep the graph, commits come as git
// finds them, newest first, without parents, so that the first appear
// before git has searched all of history.
func Filtered(ctx context.Context, r *run.Runner, gitDir string, tips [][]byte, f Filter, s *commits.Store, fn func(id uint32, parents []uint32) error) error {
	if len(tips) == 0 {
		return nil
	}
	var in bytes.Buffer
	for _, t := range tips {
		fmt.Fprintf(&in, "%x\n", t)
	}
	args := []string{"log", "--format=%ct %H %P", "--no-color", "--no-ext-diff", "--no-textconv", "--stdin"}
	graph := f.KeepsGraph()
	if graph {
		args = append(args, "--topo-order", "--parents")
	}
	args = append(append(args, f.args()...), "--")
	args = append(args, f.Paths...)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	p := &revListParser{s: s, oid: make([]byte, 64), cancel: cancel, fn: fn}
	switch {
	case !graph:
		p.fn = func(id uint32, _ []uint32) error { return fn(id, nil) }
	case f.FirstParent:
		// git still prints every parent of a merge it follows one way.
		p.fn = func(id uint32, parents []uint32) error { return fn(id, parents[:min(len(parents), 1)]) }
	}
	_, err := r.Run(ctx, &run.Cmd{
		GitDir: gitDir, Args: args, Read: true, Stdin: &in, Stdout: p, Timeout: RevListTimeout,
		Env: []string{"GIT_LITERAL_PATHSPECS=1"},
	})
	if p.err != nil {
		return p.err
	}
	if err != nil {
		return err
	}
	if len(p.partial) > 0 {
		return fmt.Errorf("history: git log output ends mid-line")
	}
	return nil
}
