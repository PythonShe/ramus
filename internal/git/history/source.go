package history

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"

	"ramus/internal/git/catfile"
	"ramus/internal/git/commitgraph"
	"ramus/internal/git/commits"
	"ramus/internal/git/object"
)

// genInfinity is the generation of a commit the graph does not hold, as
// git gives it: above every commit the graph holds, so a walk counts the
// children of all of them first.
const genInfinity = ^uint64(0)

// ErrTooManyRead is returned by Source.Resolve when more commits than its
// limit would have to be read from their objects: git orders such a
// history faster than cat-file reads it.
var ErrTooManyRead = errors.New("history: too many commits outside the commit-graph")

// Source is the commits history is ordered from: those of the
// commit-graph, numbered by their positions in it, and those it does not
// hold, read from their objects and numbered after them. A commit in the
// graph has its parents there too, so the commits read are the newest:
// made since the graph was written, down to where they reach it.
//
// Without a graph, it is the commits git listed (RevList) in its store,
// for new ones to be read on top of them.
//
// Its methods are for the one goroutine that walks it; OIDs reads object
// ids from any.
type Source struct {
	g    *commitgraph.Graph
	base uint32
	// more holds the commits read. Their parents are kept as numbers of
	// this source, graph positions included, not as the store's own.
	more    *commits.Store
	scratch []uint32
}

// NewSource returns a source over g, which may be nil, and the commits
// in more, a store whose parents are numbered as this source numbers
// them (graph positions, then the store's numbers after them); nil makes
// an empty one for object ids of hashSize bytes.
func NewSource(g *commitgraph.Graph, more *commits.Store, hashSize int) *Source {
	if more == nil {
		more = commits.New(hashSize)
	}
	s := &Source{g: g, more: more}
	if g != nil {
		s.base = uint32(g.Len())
	}
	return s
}

// Graph returns the commit-graph the source numbers from, or nil.
func (s *Source) Graph() *commitgraph.Graph { return s.g }

// Len returns the number of commits numbered: the graph's and those read.
func (s *Source) Len() int { return int(s.base) + s.more.Len() }

// Read returns the number of commits numbered outside the graph.
func (s *Source) Read() int { return s.more.Len() }

// Lookup returns the number of the commit oid, if the source holds it.
func (s *Source) Lookup(oid []byte) (uint32, bool) {
	if s.g != nil {
		if p := s.g.Lookup(oid); p != commitgraph.None {
			return p, true
		}
	}
	id, ok := s.more.Lookup(oid)
	return s.base + id, ok
}

// OID returns the object id of commit id, on the walking goroutine.
func (s *Source) OID(id uint32) []byte {
	if id < s.base {
		return s.g.OID(id)
	}
	return s.more.OID(id - s.base)
}

// OIDs returns a reader of the object ids of the commits numbered so far,
// safe from any goroutine while the source goes on reading commits. Its
// bytes may point into the commit-graph's mapping: see HistoryState.OID.
func (s *Source) OIDs() func(id uint32) []byte {
	g, base, snap := s.g, s.base, s.more.Snapshot()
	return func(id uint32) []byte {
		if id < base {
			return g.OID(id)
		}
		return snap.OID(id - base)
	}
}

// Parents appends the parents of commit id to dst.
func (s *Source) Parents(id uint32, dst []uint32) ([]uint32, error) {
	if id < s.base {
		return s.g.Parents(id, dst)
	}
	out, ok := s.more.Parents(id-s.base, dst)
	if !ok {
		return dst, fmt.Errorf("history: commit %x was not read", s.more.OID(id-s.base))
	}
	return out, nil
}

// Generation returns the commit's generation in the graph, genInfinity
// for one read from its object.
func (s *Source) Generation(id uint32) uint64 {
	if id < s.base {
		return s.g.Generation(id)
	}
	return genInfinity
}

// CommitTime returns the committer time of commit id.
func (s *Source) CommitTime(id uint32) int64 {
	if id < s.base {
		return s.g.CommitTime(id)
	}
	return s.more.CommitTime(id - s.base)
}

// known reports whether commit id has its parents recorded: in the graph,
// or read.
func (s *Source) known(id uint32) bool {
	if id < s.base {
		return true
	}
	return s.more.Added(id - s.base)
}

// readBatch bounds the commits asked of cat-file at once.
const readBatch = 1024

// Resolve numbers the commits tips name, in their order, reading through
// cat-file each one the source does not hold yet and every commit it
// reaches until the walk down meets commits the source holds. A tip that
// is not a commit (a tag may name a tree) is left out. At most limit
// commits are read; past that it returns ErrTooManyRead.
func (s *Source) Resolve(ctx context.Context, cat *catfile.Process, tips [][]byte, limit int) ([]uint32, error) {
	var want [][]byte
	var wantTip []bool
	for _, t := range tips {
		if id, ok := s.Lookup(t); !ok || !s.known(id) {
			want, wantTip = append(want, t), append(wantTip, true)
		}
	}
	read := 0
	for len(want) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		batch := want[:min(len(want), readBatch)]
		batchTip := wantTip[:len(batch)]
		want, wantTip = want[len(batch):], wantTip[len(batch):]
		var next [][]byte
		err := cat.Read(batch, func(i int, o catfile.Object) error {
			oid := batch[i]
			if id, ok := s.Lookup(oid); ok && s.known(id) {
				return nil // read already, through another child
			}
			if o.Type != "commit" {
				if batchTip[i] && !o.Missing {
					return nil // a tag of a tree or a blob
				}
				return fmt.Errorf("history: commit %x cannot be read", oid)
			}
			if read++; read > limit {
				return ErrTooManyRead
			}
			// What git orders by is the parents and the committer's time;
			// a commit damaged elsewhere still has them.
			c, _ := object.ParseCommit(o.Data)
			id := s.more.ID(oid)
			ps := s.scratch[:0]
			for _, h := range object.ParentIDs(o.Data) {
				pid := make([]byte, len(h)/2)
				if _, err := hex.Decode(pid, h); err != nil || len(pid) != s.more.Width() {
					return fmt.Errorf("history: commit %x names parent %q", oid, h)
				}
				p, ok := s.Lookup(pid)
				if !ok {
					p = s.base + s.more.ID(pid)
				}
				if !s.known(p) {
					next = append(next, pid)
				}
				ps = append(ps, p)
			}
			s.scratch = ps
			return s.more.Add(id, ps, c.Committer.When)
		})
		if err != nil {
			return nil, err
		}
		for _, p := range next {
			want, wantTip = append(want, p), append(wantTip, false)
		}
	}
	var ids []uint32
	for _, t := range tips {
		if id, ok := s.Lookup(t); ok && s.known(id) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// Covered returns the graph positions of tips when the commit-graph holds
// every one of them, so a walk of the graph alone (Walk, or TopoWalk over
// a Source that read nothing) orders history from them. g may be nil, for
// a repository without one or whose graph does not describe its history
// (repo.Capabilities.CommitGraphUsable). A graph without generation
// numbers covers nothing: the walks order by them.
func Covered(g *commitgraph.Graph, tips [][]byte) ([]uint32, bool) {
	if g == nil || !g.HasGenerations() {
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
