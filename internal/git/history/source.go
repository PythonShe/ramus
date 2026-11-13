package history

import "ramus/internal/git/commitgraph"

// Covered returns the graph positions of tips when the commit-graph holds
// every one of them, so TopoWalk can order history natively. A tip missing
// from it (commits made since it was written) means the graph is stale,
// and RevList orders history instead; git then uses the graph for the part
// it covers. g may be nil, for a repository without one or whose graph
// does not describe its history (repo.Capabilities.CommitGraphUsable).
func Covered(g *commitgraph.Graph, tips [][]byte) ([]uint32, bool) {
	if g == nil {
		return nil, false
	}
	pos := make([]uint32, 0, len(tips))
	for _, t := range tips {
		p := g.Lookup(t)
		if p == commitgraph.None {
			return nil, false
		}
		pos = append(pos, p)
	}
	return pos, true
}
