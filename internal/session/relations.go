package session

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"

	"ramus/internal/git/catfile"
	"ramus/internal/git/commitgraph"
	"ramus/internal/git/history"
	"ramus/internal/git/refs"
	"ramus/internal/git/repo"
)

// CommitRelations are the refs that contain a commit and its children.
type CommitRelations struct {
	Refs     []string // full names: refs/heads/x, refs/remotes/o/x, refs/tags/t; local branches first, then tags, then remotes, each sorted
	More     int      // refs containing it beyond those listed (cap the list at 100)
	Children [][]byte // object ids, newest first
	Err      error
}

// maxRelationRefs bounds the refs a CommitRelations lists: an old commit of
// a large project is in every one of thousands of tags.
const maxRelationRefs = 100

// LoadCommitRelations finds the branches, remote branches and tags that
// contain commit oid, and its children among the commits they and HEAD
// reach, off the main thread, and hands them to done on it, once. cancel
// stops the search; done is not called after it. What was found for the
// refs and HEAD as they are is handed over at once. A search whose
// repository closed and opened again (Trust) ends with ErrReopened.
func (s *Session) LoadCommitRelations(oid []byte, done func(CommitRelations)) (cancel func()) {
	r := s.Repo
	if r == nil {
		done(CommitRelations{Err: errors.New("session: the repository is not open")})
		return func() {}
	}
	list, gen, head := s.State.Refs, s.refsGen, bytes.Clone(s.State.Head.OID)
	key := relationsKey(oid)
	if a, ok := s.answers().Get(key); ok && a.relations != nil && a.relationsRefs == gen && bytes.Equal(a.relationsHead, head) {
		done(*a.relations)
		return func() {}
	}
	ctx, stop := context.WithCancel(s.viewContext(r))
	// dropped is read and written on the main thread only.
	dropped := false
	reopened := func() {
		if !dropped {
			done(CommitRelations{Err: ErrReopened})
		}
	}
	r.Life().Go(func(context.Context) {
		defer stop()
		rel := readRelations(ctx, r, list, head, oid)
		if ctx.Err() != nil {
			if closedUnder(r) {
				s.handOver(r, nil, reopened)
			}
			return // cancelled, or the repository closed
		}
		s.handOver(r, func() {
			if rel.Err == nil {
				s.keep(r, key, answer{relations: &rel, relationsRefs: gen, relationsHead: head})
			}
			if !dropped {
				done(rel)
			}
		}, reopened)
	})
	return func() { dropped = true; stop() }
}

func relationsKey(oid []byte) string { return "r\x00" + string(oid) }

// readRelations finds the relations of commit oid natively from the
// commit-graph, the commits made since it was written read from their
// objects, or, without a graph or with more of those than git would take
// to walk, through git.
func readRelations(ctx context.Context, r *repo.Repository, list *refs.List, head, oid []byte) CommitRelations {
	// The walk and git share one bound: on a large repository without a
	// commit-graph, git reads every commit above an old one.
	ctx, cancel := context.WithTimeout(ctx, history.RelationsTimeout)
	defer cancel()
	cat, err := r.CatFile()
	if err != nil {
		return CommitRelations{Err: err}
	}
	names, tips, err := relationTips(cat, list)
	if err != nil {
		return CommitRelations{Err: err}
	}
	// HEAD reaches children too, detached on a commit no branch has.
	all := tips
	if head != nil {
		all = append(slices.Clip(tips), head)
	}
	g, err := r.CommitGraph()
	if err != nil {
		return CommitRelations{Err: err}
	}
	if list == nil {
		// Refs read through git, in a storage the engine does not read:
		// git says which contain the commit, and HEAD's children are
		// those found.
		return gitRelations(ctx, r, cat, g, oid, all, nil)
	}
	if g != nil && g.HasGenerations() {
		rel, err := history.RelationsOf(ctx, history.NewSource(g, nil, r.Caps.HashSize), cat, oid, all, staleRead)
		if err == nil {
			var in []string
			for i, name := range names {
				if rel.Reach[i] {
					in = append(in, name)
				}
			}
			return listRelations(in, rel.Children)
		}
		if ctx.Err() != nil {
			return CommitRelations{Err: ctx.Err()}
		}
		// Past the limit, or a commit that cannot be read: git walks it,
		// or says what is wrong.
	}
	// Not nil: a list without branches or tags lists none.
	return gitRelations(ctx, r, cat, g, oid, all, append([]string{}, names...))
}

// gitRelations finds the relations of commit oid through git, among the
// refs names when not nil: git lists symbolic refs too
// (refs/remotes/origin/HEAD), and may have seen refs moved since the
// list was read. Without names, those of other kinds and remotes' HEADs
// are left out.
func gitRelations(ctx context.Context, r *repo.Repository, cat *catfile.Process, g *commitgraph.Graph, oid []byte, all [][]byte, names []string) CommitRelations {
	in, err := history.RefsContainingGit(ctx, r.Runner, r.GitDir, oid)
	if err != nil {
		return CommitRelations{Err: err}
	}
	if names != nil {
		listed := map[string]bool{}
		for _, n := range names {
			listed[n] = true
		}
		in = slices.DeleteFunc(in, func(n string) bool { return !listed[n] })
	} else {
		in = slices.DeleteFunc(in, func(n string) bool {
			return refKind(n) < 0 || refKind(n) == 2 && strings.HasSuffix(n, "/HEAD")
		})
	}
	commits, err := commitsOf(cat, g, all)
	if err != nil {
		return CommitRelations{Err: err}
	}
	children, err := history.ChildrenGit(ctx, r.Runner, r.GitDir, oid, commits)
	if err != nil {
		return CommitRelations{Err: err}
	}
	return listRelations(in, children)
}

// relationTips are the branches, remote branches and tags of list, by full
// name, symbolic ones left out, with the object each names in the end: an
// annotated tag peeled, through tags of tags, maybe to a tree.
func relationTips(cat *catfile.Process, list *refs.List) (names []string, tips [][]byte, err error) {
	if list == nil {
		return nil, nil, nil
	}
	// loose are the tags without a peeled value, by their index in names:
	// they may be annotated.
	var loose []int
	for i := range list.Len() {
		name := list.Name(i)
		if refKind(name) < 0 || list.Symbolic(i) != "" {
			continue
		}
		tip := list.OID(i)
		if strings.HasPrefix(name, "refs/tags/") {
			if p := list.Peeled(i); p != nil {
				tip = p
			} else if !list.PeelKnown(i) {
				loose = append(loose, len(names))
			}
		}
		names, tips = append(names, name), append(tips, tip)
	}
	for depth := 0; len(loose) > 0 && depth < 8; depth++ {
		ids := make([][]byte, len(loose))
		for k, j := range loose {
			ids[k] = tips[j]
		}
		var next []int
		err := cat.Read(ids, func(k int, o catfile.Object) error {
			if o.Type == "tag" {
				if t := tagTarget(o.Data); t != nil {
					tips[loose[k]] = t
					next = append(next, loose[k])
				}
			}
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
		loose = next
	}
	return names, tips, nil
}

// commitsOf keeps the tips that are commits, without repeats: those in
// g, which may be nil, and those cat-file says are.
func commitsOf(cat *catfile.Process, g *commitgraph.Graph, tips [][]byte) ([][]byte, error) {
	var keep, check [][]byte
	seen := map[string]bool{}
	for _, t := range tips {
		if seen[string(t)] {
			continue
		}
		seen[string(t)] = true
		if g != nil && g.Lookup(t) != commitgraph.None {
			keep = append(keep, t)
		} else {
			check = append(check, t)
		}
	}
	err := cat.Read(check, func(i int, o catfile.Object) error {
		if o.Type == "commit" {
			keep = append(keep, check[i])
		}
		return nil
	})
	return keep, err
}

// refKind orders refs as CommitRelations lists them: local branches, tags,
// remote branches; -1 for any other ref.
func refKind(name string) int {
	switch {
	case strings.HasPrefix(name, "refs/heads/"):
		return 0
	case strings.HasPrefix(name, "refs/tags/"):
		return 1
	case strings.HasPrefix(name, "refs/remotes/"):
		return 2
	}
	return -1
}

// listRelations orders names as CommitRelations lists them, the first
// maxRelationRefs of them.
func listRelations(names []string, children [][]byte) CommitRelations {
	slices.SortFunc(names, func(a, b string) int {
		if c := cmp.Compare(refKind(a), refKind(b)); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	names = slices.Compact(names)
	rel := CommitRelations{Refs: names, Children: children}
	if len(names) > maxRelationRefs {
		// A copy, so the names left out are not kept with the answer.
		rel.Refs, rel.More = slices.Clone(names[:maxRelationRefs]), len(names)-maxRelationRefs
	}
	return rel
}
