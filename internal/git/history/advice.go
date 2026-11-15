package history

import (
	"ramus/internal/git/commitgraph"
	"ramus/internal/git/run"
)

// Advice is a commit-graph write that would make history faster.
type Advice struct {
	// Replace is set for a write that rewrites the whole chain, as
	// GraphWrite's replace: on a large repository it takes a while.
	Replace bool
	Cmd     run.Cmd
}

// GraphAdvice says whether writing the commit-graph would make history
// faster. usable is repo.Capabilities.CommitGraphUsable: with grafts,
// replace refs or a shallow clone git does not use a graph, so writing one
// would not help. g is the open graph or nil; covered is what Covered
// reported for it.
func GraphAdvice(gitDir string, usable bool, g *commitgraph.Graph, covered bool) (Advice, bool) {
	replace := false
	switch {
	case !usable:
		return Advice{}, false
	case g == nil:
		// git reads every commit to order history.
	case !g.HasGenerations():
		// git reads every commit to order history, and the layers
		// without generation numbers must go, not only gain one on top.
		replace = true
	case !covered:
		// The commits made since are read from their objects.
	case g.Bloom().Version == 0:
		// The history of a file is slow without changed-path filters. A
		// plain split write adds a layer only for new commits; filters
		// for the old ones need the chain rewritten.
		replace = true
	default:
		return Advice{}, false
	}
	return Advice{Replace: replace, Cmd: GraphWrite(gitDir, replace)}, true
}

// GraphWrite is the command that writes the commit-graph for every commit
// reachable from a ref, with changed-path filters: a layer on top of the
// chain for the commits it lacks, or, with replace, the chain anew.
func GraphWrite(gitDir string, replace bool) run.Cmd {
	split := "--split"
	if replace {
		split = "--split=replace"
	}
	return run.Cmd{
		GitDir:  gitDir,
		Args:    []string{"commit-graph", "write", "--reachable", split, "--changed-paths"},
		Timeout: RevListTimeout,
	}
}
