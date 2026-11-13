// Package diff gets diffs and changed-file lists: from git where its
// settings (renames, filters, textconv, diff.algorithm) shape the result,
// and natively where only object ids are involved.
package diff

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
)

// maxHeader bounds a raw line's header: two modes, two object ids and a
// status are under 160 bytes even for SHA-256.
const maxHeader = 256

// ErrMalformed is returned for raw output that cannot be parsed.
var ErrMalformed = errors.New("diff: malformed raw output")

// Raw is a changed-file list as git diff --raw -z prints it, in flat
// storage. Status letters are git's: A, C, D, M, R, T, U, X.
type Raw struct {
	hashSize int
	paths    []byte
	ends     []uint32 // two per entry: old path end, new path end
	status   []byte
	score    []uint8
	modes    []uint32 // old, new
	oids     []byte   // old, new
}

// Len returns the number of entries.
func (r *Raw) Len() int { return len(r.status) }

// Status returns entry i's status letter.
func (r *Raw) Status(i int) byte { return r.status[i] }

// Score returns the similarity of a rename or copy, 0 to 100.
func (r *Raw) Score(i int) int { return int(r.score[i]) }

// Path returns entry i's path after the change.
func (r *Raw) Path(i int) []byte { return r.path(2*i + 1) }

// OldPath returns entry i's path before the change; it differs from Path
// only for a rename or copy.
func (r *Raw) OldPath(i int) []byte { return r.path(2 * i) }

func (r *Raw) path(k int) []byte {
	start := uint32(0)
	if k > 0 {
		start = r.ends[k-1]
	}
	return r.paths[start:r.ends[k]:r.ends[k]]
}

// Old returns the mode and object id before.
func (r *Raw) Old(i int) (uint32, []byte) {
	o := 2 * i * r.hashSize
	return r.modes[2*i], r.oids[o : o+r.hashSize : o+r.hashSize]
}

// New returns the mode and object id after; the id is zero for a work
// tree file git has not hashed.
func (r *Raw) New(i int) (uint32, []byte) {
	o := (2*i + 1) * r.hashSize
	return r.modes[2*i+1], r.oids[o : o+r.hashSize : o+r.hashSize]
}

// Bytes returns the memory the list holds.
func (r *Raw) Bytes() int {
	return cap(r.paths) + cap(r.ends)*4 + cap(r.status) + cap(r.score) + cap(r.modes)*4 + cap(r.oids)
}

// ParseRaw reads the output of git diff-tree, diff-index or diff-files
// with --raw -z --no-abbrev. A leading commit id line, which diff-tree
// prints for a single commit, is skipped.
func ParseRaw(data []byte, hashSize int) (*Raw, error) {
	r := &Raw{hashSize: hashSize}
	next := func() ([]byte, bool) {
		i := bytes.IndexByte(data, 0)
		if i < 0 {
			return nil, false
		}
		f := data[:i]
		data = data[i+1:]
		return f, true
	}
	for len(data) > 0 {
		head, ok := next()
		if !ok {
			return nil, ErrMalformed
		}
		if len(head) > maxHeader {
			return nil, fmt.Errorf("%w: header of %d bytes", ErrMalformed, len(head))
		}
		if len(head) == 0 || head[0] != ':' {
			if len(head) == 2*hashSize && r.Len() == 0 {
				continue // the commit diff-tree names
			}
			return nil, fmt.Errorf("%w: %q", ErrMalformed, head)
		}
		f := bytes.Fields(head[1:])
		if len(f) != 5 || len(f[4]) == 0 {
			return nil, fmt.Errorf("%w: %q", ErrMalformed, head)
		}
		var modes [2]uint32
		for k := range modes {
			m, err := strconv.ParseUint(string(f[k]), 8, 32)
			if err != nil {
				return nil, fmt.Errorf("%w: mode %q", ErrMalformed, f[k])
			}
			modes[k] = uint32(m)
		}
		for _, h := range f[2:4] {
			if len(h) != 2*hashSize {
				return nil, fmt.Errorf("%w: object id %q", ErrMalformed, h)
			}
			o := len(r.oids)
			r.oids = append(r.oids, make([]byte, hashSize)...)
			if _, err := hex.Decode(r.oids[o:], h); err != nil {
				return nil, fmt.Errorf("%w: object id %q", ErrMalformed, h)
			}
		}
		st := f[4][0]
		score := 0
		if len(f[4]) > 1 {
			s, err := strconv.Atoi(string(f[4][1:]))
			if err != nil || s < 0 || s > 100 {
				return nil, fmt.Errorf("%w: status %q", ErrMalformed, f[4])
			}
			score = s
		}
		src, ok := next()
		if !ok {
			return nil, ErrMalformed
		}
		dst := src
		if st == 'R' || st == 'C' {
			if dst, ok = next(); !ok {
				return nil, ErrMalformed
			}
		}
		r.paths = append(r.paths, src...)
		r.ends = append(r.ends, uint32(len(r.paths)))
		r.paths = append(r.paths, dst...)
		r.ends = append(r.ends, uint32(len(r.paths)))
		r.status = append(r.status, st)
		r.score = append(r.score, uint8(score))
		r.modes = append(r.modes, modes[0], modes[1])
	}
	return r, nil
}
