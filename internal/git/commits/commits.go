// Package commits keeps commits the commit-graph does not describe, for
// histories that git orders: each commit gets a number, its object id and
// parents go in flat chunks, and nothing per commit holds a pointer, so a
// history of millions of commits costs the garbage collector nothing.
//
// Text (subjects, authors) is not here: it loads by row through cat-file
// into history.TextChunk, one shared buffer per chunk of rows, so the
// cache can drop it under its byte budget.
package commits

import (
	"encoding/binary"
	"errors"
)

const (
	chunkBits   = 14
	chunkLen    = 1 << chunkBits
	parentChunk = 1 << 16
	// MaxParents bounds one commit's parent list; git has no limit, but a
	// list past this is not a history anyone can draw.
	MaxParents = 1 << 15
)

// ErrTooManyParents is returned for a parent list past MaxParents.
var ErrTooManyParents = errors.New("commits: too many parents")

const unknown = ^uint16(0)

type meta struct {
	parents uint32 // index of the first parent in the parent chunks
	np      uint16 // number of parents; unknown until the commit is added
	_       uint16
	time    int64 // committer time
}

type store struct {
	oids    [][]byte
	meta    [][]meta
	parents [][]uint32
	n       int
	np      int // parents used
	width   int
}

// Store is the writer's side. It is not safe for use by several
// goroutines; readers take a Snapshot.
type Store struct {
	s     store
	index []uint32 // open addressing: id+1, 0 when empty
}

// New returns an empty store for object ids of width bytes (20 or 32).
func New(width int) *Store {
	return &Store{s: store{width: width}, index: make([]uint32, 1024)}
}

// Width returns the object id width in bytes.
func (s *Store) Width() int { return s.s.width }

// Len returns the number of commits numbered so far, including parents
// that have been named but not yet added.
func (s *Store) Len() int { return s.s.n }

// Bytes returns the memory the store holds.
func (s *Store) Bytes() int {
	return len(s.s.oids)*chunkLen*s.s.width + len(s.s.meta)*chunkLen*16 +
		len(s.s.parents)*parentChunk*4 + len(s.index)*4
}

func (s *Store) slot(oid []byte) uint32 {
	// Object ids are uniform already; their first bytes hash them.
	return uint32(binary.LittleEndian.Uint64(oid)) & uint32(len(s.index)-1)
}

// Lookup returns the number of a commit seen before.
func (s *Store) Lookup(oid []byte) (uint32, bool) {
	if len(oid) != s.s.width {
		return 0, false
	}
	for i := s.slot(oid); ; i = (i + 1) & uint32(len(s.index)-1) {
		v := s.index[i]
		if v == 0 {
			return 0, false
		}
		if string(s.s.oid(v-1)) == string(oid) {
			return v - 1, true
		}
	}
}

// ID returns the number of oid, giving it the next one if it is new.
func (s *Store) ID(oid []byte) uint32 {
	if id, ok := s.Lookup(oid); ok {
		return id
	}
	id := uint32(s.s.n)
	if id%chunkLen == 0 {
		s.s.oids = append(s.s.oids, make([]byte, chunkLen*s.s.width))
		s.s.meta = append(s.s.meta, make([]meta, chunkLen))
	}
	copy(s.s.oids[id>>chunkBits][int(id&(chunkLen-1))*s.s.width:], oid)
	s.s.meta[id>>chunkBits][id&(chunkLen-1)].np = unknown
	s.s.n++
	if s.s.n*2 > len(s.index) {
		s.grow()
	}
	s.insert(id)
	return id
}

func (s *Store) insert(id uint32) {
	i := s.slot(s.s.oid(id))
	for s.index[i] != 0 {
		i = (i + 1) & uint32(len(s.index)-1)
	}
	s.index[i] = id + 1
}

func (s *Store) grow() {
	s.index = make([]uint32, len(s.index)*2)
	for id := 0; id < s.s.n-1; id++ {
		s.insert(uint32(id))
	}
}

// Add records commit id's parents and committer time. The parents are
// numbers from ID.
func (s *Store) Add(id uint32, parents []uint32, time int64) error {
	if len(parents) > MaxParents {
		return ErrTooManyParents
	}
	off := s.s.np % parentChunk
	if s.s.np == len(s.s.parents)*parentChunk || off+len(parents) > parentChunk {
		// A list never straddles two chunks.
		s.s.parents = append(s.s.parents, make([]uint32, parentChunk))
		s.s.np = (len(s.s.parents) - 1) * parentChunk
		off = 0
	}
	copy(s.s.parents[len(s.s.parents)-1][off:], parents)
	m := &s.s.meta[id>>chunkBits][id&(chunkLen-1)]
	m.parents, m.np, m.time = uint32(s.s.np), uint16(len(parents)), time
	s.s.np += len(parents)
	return nil
}

// Snapshot returns a view of the object ids numbered so far, for another
// goroutine (the UI loading text by row); it stays valid while the writer
// goes on. Parents and times stay on the writer's side: a commit named as a
// parent gets them later, while a reader might be looking.
func (s *Store) Snapshot() *Snapshot {
	return &Snapshot{oids: s.s.oids[:len(s.s.oids):len(s.s.oids)], n: s.s.n, width: s.s.width}
}

// OID returns commit id's object id.
func (s *Store) OID(id uint32) []byte { return s.s.oid(id) }

// Parents appends commit id's parents to dst; ok is false for a commit
// named as a parent but not added.
func (s *Store) Parents(id uint32, dst []uint32) ([]uint32, bool) { return s.s.parentsOf(id, dst) }

// CommitTime returns commit id's committer time, 0 if not added.
func (s *Store) CommitTime(id uint32) int64 { return s.s.m(id).time }

func (s *store) oid(id uint32) []byte {
	off := int(id&(chunkLen-1)) * s.width
	return s.oids[id>>chunkBits][off : off+s.width : off+s.width]
}

func (s *store) m(id uint32) *meta { return &s.meta[id>>chunkBits][id&(chunkLen-1)] }

func (s *store) parentsOf(id uint32, dst []uint32) ([]uint32, bool) {
	m := s.m(id)
	if m.np == unknown {
		return dst, false
	}
	c, off := m.parents/parentChunk, m.parents%parentChunk
	return append(dst, s.parents[c][off:off+uint32(m.np)]...), true
}

// Snapshot reads the object ids numbered when it was taken.
type Snapshot struct {
	oids  [][]byte
	n     int
	width int
}

// Len returns the number of commits the snapshot holds.
func (s *Snapshot) Len() int { return s.n }

// OID returns commit id's object id.
func (s *Snapshot) OID(id uint32) []byte {
	off := int(id&(chunkLen-1)) * s.width
	return s.oids[id>>chunkBits][off : off+s.width : off+s.width]
}
