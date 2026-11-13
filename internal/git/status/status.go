// Package status reads the work tree's status from one git status call
// into flat storage: one buffer for paths, fixed-size records for the
// rest, so a status of a million untracked files is two allocations.
package status

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"

	"ramus/internal/git/run"
)

// Kind is the kind of a status entry, as porcelain v2 marks it.
type Kind byte

const (
	Changed   Kind = '1'
	Renamed   Kind = '2' // renamed or copied; Score says which and how close
	Unmerged  Kind = 'u'
	Untracked Kind = '?'
	Ignored   Kind = '!'
)

// Branch is what status says about HEAD.
type Branch struct {
	// OID is HEAD's commit, empty for a branch with no commits yet.
	OID string
	// Head is the branch name; empty when detached.
	Head     string
	Detached bool
	// Upstream is the upstream branch, as "origin/main"; empty when none.
	Upstream string
	// Stashes is the number of stash entries.
	Stashes int
}

type entry struct {
	kind    Kind
	x, y    byte
	sub     [4]byte
	score   byte
	modes   [4]uint32 // HEAD, index, work tree; unmerged: stages 1-3, work tree
	oidOff  uint32    // first object id in oids
	pathEnd uint32
	origEnd uint32 // after the original path of a rename; == pathEnd otherwise
}

// Status is a parsed git status.
type Status struct {
	Branch   Branch
	hashSize int
	entries  []entry
	paths    []byte
	oids     []byte
}

// Len returns the number of entries.
func (s *Status) Len() int { return len(s.entries) }

// Bytes returns the memory the status holds.
func (s *Status) Bytes() int {
	return cap(s.entries)*48 + cap(s.paths) + cap(s.oids)
}

// Kind returns entry i's kind.
func (s *Status) Kind(i int) Kind { return s.entries[i].kind }

// XY returns entry i's index and work tree states: '.', M, T, A, D, R, C
// or U, as git status prints them; untracked and ignored entries give ?
// and !.
func (s *Status) XY(i int) (x, y byte) { return s.entries[i].x, s.entries[i].y }

// Submodule returns entry i's submodule state: "N..." for a path that is
// not a submodule, else S then C (commit changed), M (tracked changes)
// and U (untracked files), or dots.
func (s *Status) Submodule(i int) string { return string(s.entries[i].sub[:]) }

// Score returns a rename or copy entry's similarity and whether it is a
// copy.
func (s *Status) Score(i int) (score int, copy bool) {
	e := s.entries[i]
	return int(e.score &^ 0x80), e.score&0x80 != 0
}

// Path returns entry i's path.
func (s *Status) Path(i int) []byte {
	start := uint32(0)
	if i > 0 {
		start = s.entries[i-1].origEnd
	}
	end := s.entries[i].pathEnd
	return s.paths[start:end:end]
}

// OrigPath returns the path a renamed or copied entry came from; nil for
// other entries.
func (s *Status) OrigPath(i int) []byte {
	e := s.entries[i]
	if e.origEnd == e.pathEnd {
		return nil
	}
	return s.paths[e.pathEnd:e.origEnd:e.origEnd]
}

// Modes returns entry i's modes: HEAD, index and work tree for a changed
// or renamed entry; stages 1, 2, 3 and the work tree for an unmerged one.
func (s *Status) Modes(i int) [4]uint32 { return s.entries[i].modes }

// OIDs returns entry i's object ids: HEAD and index for a changed or
// renamed entry, stages 1 to 3 for an unmerged one, none otherwise.
func (s *Status) OIDs(i int) [][]byte {
	e := s.entries[i]
	n := 0
	switch e.kind {
	case Changed, Renamed:
		n = 2
	case Unmerged:
		n = 3
	}
	out := make([][]byte, n)
	for k := range out {
		o := int(e.oidOff) + k*s.hashSize
		out[k] = s.oids[o : o+s.hashSize : o+s.hashSize]
	}
	return out
}

// Options choose what status lists.
type Options struct {
	// Ignored lists ignored files too.
	Ignored bool
	// UntrackedAll lists every untracked file instead of collapsing a
	// directory that holds only untracked files.
	UntrackedAll bool
}

// MaxEntries bounds one status; past it the status is refused rather than
// held, and the app says so.
const MaxEntries = 1 << 23

// ErrTooMany is returned for a status past MaxEntries.
var ErrTooMany = errors.New("status: too many entries")

// Read runs git status once: porcelain v2, branch and stash headers,
// without ahead and behind counts (computed from the commit graph off this
// path) and without looking inside submodules' own work trees.
func Read(ctx context.Context, r *run.Runner, gitDir, workTree string, hashSize int, opt Options) (*Status, error) {
	// Renames follow the user's status.renames, as in the terminal.
	args := []string{"status", "--porcelain=v2", "-z", "--branch", "--show-stash",
		"--no-ahead-behind", "--ignore-submodules=dirty"}
	if opt.Ignored {
		args = append(args, "--ignored=matching")
	}
	if opt.UntrackedAll {
		args = append(args, "--untracked-files=all")
	}
	res, err := r.Run(ctx, &run.Cmd{GitDir: gitDir, WorkTree: workTree, Args: args, Read: true})
	if err != nil {
		return nil, err
	}
	return Parse(res.Stdout, hashSize)
}

// ErrMalformed is returned for output that cannot be parsed.
var ErrMalformed = errors.New("status: malformed porcelain v2 output")

func bad(what string, b []byte) error {
	if len(b) > 80 {
		b = b[:80]
	}
	return fmt.Errorf("%w: %s %q", ErrMalformed, what, b)
}

// Parse reads git status --porcelain=v2 -z output.
func Parse(out []byte, hashSize int) (*Status, error) {
	s := &Status{hashSize: hashSize}
	next := func() ([]byte, bool) {
		i := bytes.IndexByte(out, 0)
		if i < 0 {
			return nil, false
		}
		f := out[:i]
		out = out[i+1:]
		return f, true
	}
	for len(out) > 0 {
		line, ok := next()
		if !ok {
			return nil, bad("unterminated", out)
		}
		if len(line) < 2 {
			return nil, bad("line", line)
		}
		if line[0] == '#' {
			s.header(line[2:])
			continue
		}
		if len(s.entries) == MaxEntries {
			return nil, ErrTooMany
		}
		var err error
		switch Kind(line[0]) {
		case Untracked, Ignored:
			err = s.add(entry{kind: Kind(line[0]), x: line[0], y: line[0]}, line[2:], nil)
		case Changed:
			err = s.tracked(line, 8, Changed, nil)
		case Renamed:
			orig, ok := next()
			if !ok {
				return nil, bad("rename without its source", line)
			}
			err = s.tracked(line, 9, Renamed, orig)
		case Unmerged:
			err = s.tracked(line, 10, Unmerged, nil)
		default:
			return nil, bad("kind", line)
		}
		if err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Status) header(h []byte) {
	key, val, _ := bytes.Cut(h, []byte{' '})
	v := string(val)
	switch string(key) {
	case "branch.oid":
		if v != "(initial)" {
			s.Branch.OID = v
		}
	case "branch.head":
		if v == "(detached)" {
			s.Branch.Detached = true
		} else {
			s.Branch.Head = v
		}
	case "branch.upstream":
		s.Branch.Upstream = v
	case "stash":
		s.Branch.Stashes, _ = strconv.Atoi(v)
	}
}

// tracked parses a line of n space-separated fields before the path.
func (s *Status) tracked(line []byte, n int, kind Kind, orig []byte) error {
	f := make([][]byte, 0, n)
	rest := line
	for len(f) < n {
		var field []byte
		var ok bool
		field, rest, ok = bytes.Cut(rest, []byte{' '})
		if !ok {
			return bad("fields", line)
		}
		f = append(f, field)
	}
	path := rest
	if len(f[1]) != 2 || len(f[2]) != 4 {
		return bad("state", line)
	}
	e := entry{kind: kind, x: f[1][0], y: f[1][1]}
	copy(e.sub[:], f[2])
	nModes, nOIDs := 3, 2
	if kind == Unmerged {
		nModes, nOIDs = 4, 3
	}
	for k := 0; k < nModes; k++ {
		m, err := strconv.ParseUint(string(f[3+k]), 8, 32)
		if err != nil {
			return bad("mode", line)
		}
		e.modes[k] = uint32(m)
	}
	e.oidOff = uint32(len(s.oids))
	for k := 0; k < nOIDs; k++ {
		h := f[3+nModes+k]
		if len(h) != 2*s.hashSize {
			return bad("object id", line)
		}
		s.oids = append(s.oids, make([]byte, s.hashSize)...)
		if _, err := hex.Decode(s.oids[len(s.oids)-s.hashSize:], h); err != nil {
			return bad("object id", line)
		}
	}
	if kind == Renamed {
		sc := f[8]
		if len(sc) < 2 || (sc[0] != 'R' && sc[0] != 'C') {
			return bad("score", line)
		}
		v, err := strconv.Atoi(string(sc[1:]))
		if err != nil || v < 0 || v > 100 {
			return bad("score", line)
		}
		e.score = byte(v)
		if sc[0] == 'C' {
			e.score |= 0x80
		}
	}
	return s.add(e, path, orig)
}

func (s *Status) add(e entry, path, orig []byte) error {
	if len(path) == 0 {
		return bad("empty path", path)
	}
	if len(s.paths)+len(path)+len(orig) > 1<<31 {
		return ErrTooMany
	}
	s.paths = append(s.paths, path...)
	e.pathEnd = uint32(len(s.paths))
	s.paths = append(s.paths, orig...)
	e.origEnd = uint32(len(s.paths))
	s.entries = append(s.entries, e)
	return nil
}
