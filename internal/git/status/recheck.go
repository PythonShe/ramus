package status

import (
	"bytes"
	"context"
	"strings"

	"ramus/internal/git/run"
)

// Recheck limits: past them a full status is the cheaper run anyway, and
// git status takes paths only as arguments.
const (
	MaxRecheckPaths = 64
	maxRecheckBytes = 16 << 10
)

// CanRecheck reports whether paths, reported changed by the watcher, are
// few enough to read alone.
func CanRecheck(paths []string) bool {
	if len(paths) == 0 || len(paths) > MaxRecheckPaths {
		return false
	}
	n := 0
	for _, p := range paths {
		if p == "" || strings.HasSuffix(p, "/") {
			return false
		}
		n += len(p) + 1
	}
	return n <= maxRecheckBytes
}

// ReadPaths runs git status for paths alone, matched literally, as Read
// runs it for the whole work tree.
func ReadPaths(ctx context.Context, r *run.Runner, gitDir, workTree string, hashSize int, paths []string) (*Status, error) {
	args := []string{"status", "--porcelain=v2", "-z", "--branch", "--show-stash",
		"--no-ahead-behind", "--ignore-submodules=dirty", "--"}
	res, err := r.Run(ctx, &run.Cmd{GitDir: gitDir, WorkTree: workTree, Args: append(args, paths...), Read: true,
		Env: []string{"GIT_LITERAL_PATHSPECS=1"}})
	if err != nil {
		return nil, err
	}
	return Parse(res.Stdout, hashSize)
}

// Replace returns s with the entries of paths taken from part, a status
// read for those paths alone. ok is false when part says what only a full
// status places right, and the caller runs one: a path s does not hold
// now untracked (a create), a rename, an entry for a path not asked
// about (a folder), or a path under a folder s lists as untracked.
func (s *Status) Replace(paths []string, part *Status) (out *Status, ok bool) {
	asked := make(map[string]bool, len(paths))
	for _, p := range paths {
		asked[p] = true
	}
	was := map[string]Kind{}
	for i, e := range s.entries {
		p := s.Path(i)
		if e.kind == Untracked && bytes.HasSuffix(p, []byte("/")) {
			for _, q := range paths {
				if strings.HasPrefix(q, string(p)) {
					return nil, false
				}
			}
		}
		if asked[string(p)] || e.kind == Renamed && asked[string(s.OrigPath(i))] {
			if e.kind == Renamed {
				return nil, false
			}
			was[string(p)] = e.kind
		}
	}
	for i, e := range part.entries {
		p := string(part.Path(i))
		switch {
		case !asked[p], e.kind == Renamed, e.kind == Ignored:
			return nil, false
		case e.kind == Untracked && was[p] != Untracked:
			return nil, false
		}
	}
	out = &Status{Branch: s.Branch, hashSize: s.hashSize}
	out.entries = make([]entry, 0, len(s.entries)+len(part.entries))
	out.paths = make([]byte, 0, len(s.paths)+len(part.paths))
	out.oids = make([]byte, 0, len(s.oids)+len(part.oids))
	// git lists tracked entries by path, then untracked ones by path:
	// merge part's tracked entries into s's, in order.
	j := 0
	nextPart := func() int {
		for j < len(part.entries) && part.entries[j].kind == Untracked {
			j++
		}
		return j
	}
	for i := range s.entries {
		if s.entries[i].kind == Untracked {
			break
		}
		for k := nextPart(); k < len(part.entries) && bytes.Compare(part.Path(k), s.Path(i)) < 0; k = nextPart() {
			out.copyFrom(part, k)
			j++
		}
		if !asked[string(s.Path(i))] {
			out.copyFrom(s, i)
		}
	}
	for k := nextPart(); k < len(part.entries); k = nextPart() {
		out.copyFrom(part, k)
		j++
	}
	stillUntracked := map[string]bool{}
	for k, e := range part.entries {
		if e.kind == Untracked {
			stillUntracked[string(part.Path(k))] = true
		}
	}
	for i, e := range s.entries {
		if e.kind == Untracked && (!asked[string(s.Path(i))] || stillUntracked[string(s.Path(i))]) {
			out.copyFrom(s, i)
		}
	}
	return out, true
}

// copyFrom appends entry i of src.
func (s *Status) copyFrom(src *Status, i int) {
	e := src.entries[i]
	n := 0
	switch e.kind {
	case Changed, Renamed:
		n = 2
	case Unmerged:
		n = 3
		s.unmerged++
	}
	off := int(e.oidOff)
	e.oidOff = uint32(len(s.oids))
	s.oids = append(s.oids, src.oids[off:off+n*src.hashSize]...)
	// add cannot fail here: the entry was valid in src and out holds no
	// more than src and part together.
	_ = s.add(e, src.Path(i), src.OrigPath(i))
}
