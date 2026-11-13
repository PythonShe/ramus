// Package tree reads git tree objects and compares two trees natively:
// the paths a commit added, modified, deleted or changed the type of.
// Renames are not detected here; they come from git diff-tree -M for the
// commit that is open.
package tree

import (
	"bytes"
	"errors"
	"fmt"
)

// Modes git writes in trees.
const (
	ModeTree       = 0o040000
	ModeFile       = 0o100644
	ModeExecutable = 0o100755
	ModeSymlink    = 0o120000
	ModeGitlink    = 0o160000
)

// ErrMalformed is returned for a tree object that cannot be parsed.
var ErrMalformed = errors.New("tree: malformed tree object")

// Entry is one entry of a tree. Name and OID point into the object's data.
type Entry struct {
	Mode uint32
	Name []byte
	OID  []byte
}

// IsTree reports a subdirectory.
func (e Entry) IsTree() bool { return e.Mode&0o170000 == ModeTree }

// Iter walks a tree object's entries in order.
type Iter struct {
	data     []byte
	hashSize int
	err      error
}

// NewIter iterates data, a tree object's content.
func NewIter(data []byte, hashSize int) Iter { return Iter{data: data, hashSize: hashSize} }

// Next returns the next entry; ok is false at the end or on malformed
// data, which Err reports.
func (it *Iter) Next() (e Entry, ok bool) {
	if len(it.data) == 0 || it.err != nil {
		return Entry{}, false
	}
	sp := bytes.IndexByte(it.data, ' ')
	if sp <= 0 || sp > 7 {
		it.err = ErrMalformed
		return Entry{}, false
	}
	for _, c := range it.data[:sp] {
		if c < '0' || c > '7' {
			it.err = ErrMalformed
			return Entry{}, false
		}
		e.Mode = e.Mode<<3 | uint32(c-'0')
	}
	rest := it.data[sp+1:]
	nul := bytes.IndexByte(rest, 0)
	if nul <= 0 || len(rest) < nul+1+it.hashSize {
		it.err = ErrMalformed
		return Entry{}, false
	}
	e.Name = rest[:nul:nul]
	e.OID = rest[nul+1 : nul+1+it.hashSize : nul+1+it.hashSize]
	it.data = rest[nul+1+it.hashSize:]
	return e, true
}

// Err returns the error that ended the iteration, if any.
func (it *Iter) Err() error { return it.err }

// compare orders entries as git does: a tree's name sorts as if it ended
// in a slash.
func compare(a, b Entry) int {
	n := min(len(a.Name), len(b.Name))
	if c := bytes.Compare(a.Name[:n], b.Name[:n]); c != 0 {
		return c
	}
	ca, cb := next(a, n), next(b, n)
	switch {
	case ca < cb:
		return -1
	case ca > cb:
		return 1
	}
	return 0
}

func next(e Entry, n int) byte {
	if n < len(e.Name) {
		return e.Name[n]
	}
	if e.IsTree() {
		return '/'
	}
	return 0
}

// kind groups modes whose change git reports as a type change.
func kind(mode uint32) uint32 { return mode & 0o170000 }

func checkName(name []byte) error {
	if bytes.IndexByte(name, '/') >= 0 || string(name) == "." || string(name) == ".." {
		return fmt.Errorf("%w: entry %q", ErrMalformed, name)
	}
	return nil
}
