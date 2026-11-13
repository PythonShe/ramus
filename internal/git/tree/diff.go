package tree

import (
	"bytes"
	"errors"
	"fmt"
	"slices"

	"ramus/internal/git/catfile"
)

// Reader reads objects in batches, as catfile.Process does.
type Reader interface {
	Read(oids [][]byte, fn func(i int, o catfile.Object) error) error
}

// Status is what happened to a path, as git diff-tree --raw names it.
type Status byte

const (
	Added       Status = 'A'
	Deleted     Status = 'D'
	Modified    Status = 'M'
	TypeChanged Status = 'T'
)

// Limits on one comparison. A tree can name the same subtree many times,
// so a few kilobytes of objects can describe millions of paths; past
// MaxChanges the list stops and says so.
const (
	MaxChanges = 1 << 21
	MaxPath    = 1 << 15
)

// maxChanges is MaxChanges, lowered by tests.
var maxChanges = MaxChanges

// ErrPathTooLong is returned for a path past MaxPath.
var ErrPathTooLong = errors.New("tree: path too long")

// Changes lists changed paths sorted as git lists them, in flat storage:
// one buffer for paths, modes and object ids side by side.
type Changes struct {
	// Truncated is set when the comparison stopped at MaxChanges; the
	// list holds the changes found by then, which are not the first ones
	// in order.
	Truncated bool
	hashSize  int
	paths     []byte
	ends      []uint32
	status    []Status
	modes     []uint32 // old, new
	oids      []byte   // old, new; zero for a side that does not exist
}

// Len returns the number of changed paths.
func (c *Changes) Len() int { return len(c.ends) }

// Path returns change i's path, slash-separated from the root.
func (c *Changes) Path(i int) []byte {
	start := uint32(0)
	if i > 0 {
		start = c.ends[i-1]
	}
	return c.paths[start:c.ends[i]:c.ends[i]]
}

// Status returns what happened to change i's path.
func (c *Changes) Status(i int) Status { return c.status[i] }

// Old returns the mode and object id before; zero for an added path.
func (c *Changes) Old(i int) (uint32, []byte) {
	o := 2 * i * c.hashSize
	return c.modes[2*i], c.oids[o : o+c.hashSize : o+c.hashSize]
}

// New returns the mode and object id after; zero for a deleted path.
func (c *Changes) New(i int) (uint32, []byte) {
	o := (2*i + 1) * c.hashSize
	return c.modes[2*i+1], c.oids[o : o+c.hashSize : o+c.hashSize]
}

// Bytes returns the memory the list holds.
func (c *Changes) Bytes() int {
	return cap(c.paths) + cap(c.ends)*4 + cap(c.status) + cap(c.modes)*4 + cap(c.oids)
}

func (c *Changes) add(prefix, name []byte, st Status, oldE, newE Entry) {
	if c.Len() >= maxChanges {
		c.Truncated = true
		return
	}
	c.paths = append(c.paths, prefix...)
	c.paths = append(c.paths, name...)
	c.ends = append(c.ends, uint32(len(c.paths)))
	c.status = append(c.status, st)
	c.modes = append(c.modes, oldE.Mode, newE.Mode)
	c.oids = appendOID(c.oids, oldE.OID, c.hashSize)
	c.oids = appendOID(c.oids, newE.OID, c.hashSize)
}

func appendOID(dst, oid []byte, n int) []byte {
	if oid == nil {
		return append(dst, make([]byte, n)...)
	}
	return append(dst, oid...)
}

// sort puts the changes in git's order: full paths bytewise, which is the
// order of a recursive tree walk where a tree's name sorts with a slash.
func (c *Changes) sort() {
	idx := make([]int, c.Len())
	for i := range idx {
		idx[i] = i
	}
	slices.SortFunc(idx, func(a, b int) int { return bytes.Compare(c.Path(a), c.Path(b)) })
	out := &Changes{hashSize: c.hashSize, Truncated: c.Truncated,
		paths: make([]byte, 0, len(c.paths)), ends: make([]uint32, 0, len(c.ends)),
		status: make([]Status, 0, len(c.status)), modes: make([]uint32, 0, len(c.modes)),
		oids: make([]byte, 0, len(c.oids))}
	for _, i := range idx {
		out.paths = append(out.paths, c.Path(i)...)
		out.ends = append(out.ends, uint32(len(out.paths)))
		out.status = append(out.status, c.status[i])
		out.modes = append(out.modes, c.modes[2*i], c.modes[2*i+1])
		out.oids = append(out.oids, c.oids[2*i*c.hashSize:(2*i+2)*c.hashSize]...)
	}
	*c = *out
}

// pending is a pair of trees at one path still to compare; a nil side is a
// tree that was added or deleted whole.
type pending struct {
	prefix   []byte // the path with a trailing slash
	old, new []byte
}

// Diff compares trees a and b (object ids; nil for an empty tree) the way
// git diff-tree -r --no-renames does. Trees are read level by level, one
// batch per level, so a deep change costs one round trip per directory
// level, not per directory.
func Diff(r Reader, hashSize int, a, b []byte) (*Changes, error) {
	c := &Changes{hashSize: hashSize}
	if bytes.Equal(a, b) {
		return c, nil
	}
	level := []pending{{old: a, new: b}}
	var held []byte
	for len(level) > 0 {
		var oids [][]byte
		type side struct {
			p   int
			old bool
		}
		var who []side
		for i, p := range level {
			if p.old != nil {
				oids, who = append(oids, p.old), append(who, side{i, true})
			}
			if p.new != nil {
				oids, who = append(oids, p.new), append(who, side{i, false})
			}
		}
		var next []pending
		err := r.Read(oids, func(i int, o catfile.Object) error {
			if o.TooLarge {
				return fmt.Errorf("tree: %x is too large to compare", oids[i])
			}
			if o.Type != "tree" {
				return fmt.Errorf("tree: %x is a %q, not a tree", oids[i], o.Type)
			}
			p := level[who[i].p]
			switch {
			case who[i].old && p.new != nil:
				held = append(held[:0], o.Data...)
				return nil
			case who[i].old:
				return walkOne(c, &next, p.prefix, o.Data, hashSize, Deleted)
			case p.old != nil:
				return compareTrees(c, &next, p.prefix, held, o.Data, hashSize)
			default:
				return walkOne(c, &next, p.prefix, o.Data, hashSize, Added)
			}
		})
		if err != nil {
			return nil, err
		}
		if c.Truncated {
			break
		}
		level = next
	}
	c.sort()
	return c, nil
}

func sub(prefix, name []byte) ([]byte, error) {
	if len(prefix)+len(name) >= MaxPath {
		return nil, ErrPathTooLong
	}
	p := make([]byte, 0, len(prefix)+len(name)+1)
	return append(append(append(p, prefix...), name...), '/'), nil
}

// walkOne lists a tree added or deleted whole.
func walkOne(c *Changes, next *[]pending, prefix, data []byte, hashSize int, st Status) error {
	it := NewIter(data, hashSize)
	for e, ok := it.Next(); ok; e, ok = it.Next() {
		if err := oneSide(c, next, prefix, e, st); err != nil {
			return err
		}
	}
	return it.Err()
}

// compareTrees merges two sorted entry lists.
func compareTrees(c *Changes, next *[]pending, prefix, a, b []byte, hashSize int) error {
	ia, ib := NewIter(a, hashSize), NewIter(b, hashSize)
	ea, okA := ia.Next()
	eb, okB := ib.Next()
	for okA || okB {
		cmp := 0
		switch {
		case !okA:
			cmp = 1
		case !okB:
			cmp = -1
		default:
			cmp = compare(ea, eb)
		}
		switch {
		case cmp < 0:
			if err := oneSide(c, next, prefix, ea, Deleted); err != nil {
				return err
			}
			ea, okA = ia.Next()
		case cmp > 0:
			if err := oneSide(c, next, prefix, eb, Added); err != nil {
				return err
			}
			eb, okB = ib.Next()
		default:
			if err := checkName(ea.Name); err != nil {
				return err
			}
			switch {
			case ea.IsTree():
				if !bytes.Equal(ea.OID, eb.OID) {
					p, err := sub(prefix, ea.Name)
					if err != nil {
						return err
					}
					*next = append(*next, pending{prefix: p, old: bytes.Clone(ea.OID), new: bytes.Clone(eb.OID)})
				}
			case kind(ea.Mode) != kind(eb.Mode):
				c.add(prefix, ea.Name, TypeChanged, ea, eb)
			case ea.Mode != eb.Mode || !bytes.Equal(ea.OID, eb.OID):
				c.add(prefix, ea.Name, Modified, ea, eb)
			}
			ea, okA = ia.Next()
			eb, okB = ib.Next()
		}
	}
	if err := ia.Err(); err != nil {
		return err
	}
	return ib.Err()
}

func oneSide(c *Changes, next *[]pending, prefix []byte, e Entry, st Status) error {
	if err := checkName(e.Name); err != nil {
		return err
	}
	if len(prefix)+len(e.Name) > MaxPath {
		return ErrPathTooLong
	}
	if e.IsTree() {
		// The entry points into data that the next read reuses.
		path, err := sub(prefix, e.Name)
		if err != nil {
			return err
		}
		if len(*next) >= maxChanges {
			c.Truncated = true
			return nil
		}
		p := pending{prefix: path}
		if st == Added {
			p.new = bytes.Clone(e.OID)
		} else {
			p.old = bytes.Clone(e.OID)
		}
		*next = append(*next, p)
		return nil
	}
	if st == Added {
		c.add(prefix, e.Name, Added, Entry{}, e)
	} else {
		c.add(prefix, e.Name, Deleted, e, Entry{})
	}
	return nil
}
