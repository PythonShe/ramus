package history

import (
	"ramus/internal/git/commitgraph"
	"ramus/internal/git/run"
)

// Advice is an offer to write the commit-graph. The app shows Cmd's exact
// line and runs it only when the user says so: it writes in the
// repository, and on a large one it takes a while.
type Advice struct {
	Reason string
	Cmd    run.Cmd
}

// GraphAdvice says whether writing the commit-graph would make history
// faster. usable is repo.Capabilities.CommitGraphUsable: with grafts,
// replace refs or a shallow clone git does not use a graph, so writing one
// would not help. g is the open graph or nil; covered is what Covered
// reported for it.
func GraphAdvice(gitDir string, usable bool, g *commitgraph.Graph, covered bool) (Advice, bool) {
	var reason string
	split := "--split"
	switch {
	case !usable:
		return Advice{}, false
	case g == nil:
		reason = "This repository has no commit-graph, so git reads every commit to order history."
	case !covered:
		reason = "The commit-graph is missing recent commits, so history is ordered by git instead."
	case g.Bloom().Version == 0:
		reason = "The commit-graph has no changed-path filters, so the history of a file is slow."
		// A plain split write adds a layer only for new commits; filters
		// for the old ones need the chain rewritten.
		split = "--split=replace"
	default:
		return Advice{}, false
	}
	return Advice{Reason: reason, Cmd: run.Cmd{
		GitDir:  gitDir,
		Args:    []string{"commit-graph", "write", "--reachable", split, "--changed-paths"},
		Timeout: RevListTimeout,
	}}, true
}
