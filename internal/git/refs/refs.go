// Package refs reads references in git's files format: loose refs,
// packed-refs, symbolic refs, and HEAD detached or unborn. Repositories
// using reftable go to git; see repo.Capabilities.
package refs

import (
	"bytes"
	"cmp"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"ramus/internal/git/osfile"
)

// maxSymrefDepth is git's limit on chains of symbolic refs.
const maxSymrefDepth = 5

// Size limits: a loose ref holds one line; packed-refs past maxPacked is
// not something git writes for any real repository.
const (
	maxLoose  = 4096
	maxPacked = 1 << 30
)

// Store reads one repository's refs.
type Store struct {
	gitDir, commonDir string
	hashSize          int
}

// New returns a store for the repository whose per-worktree git directory
// is gitDir and shared directory commonDir.
func New(gitDir, commonDir string, hashSize int) *Store {
	return &Store{gitDir: gitDir, commonDir: commonDir, hashSize: hashSize}
}

// perWorktree reports refs that live in a work tree's own git directory.
func perWorktree(name string) bool {
	return strings.HasPrefix(name, "refs/bisect/") || strings.HasPrefix(name, "refs/worktree/") ||
		strings.HasPrefix(name, "refs/rewritten/") || !strings.HasPrefix(name, "refs/")
}

func (s *Store) dirFor(name string) string {
	if perWorktree(name) {
		return s.gitDir
	}
	return s.commonDir
}

// List holds every ref under refs/, sorted by name, in flat storage.
type List struct {
	names    []byte
	ends     []uint32
	oids     []byte   // hashSize per ref
	peeled   []byte   // hashSize per ref; zero when not an annotated tag
	known    []uint64 // bit i set when PeelKnown(i)
	targets  map[int]string
	hashSize int
	// Broken names refs whose files could not be read as refs; git warns
	// about them and ignores them, and so does the list.
	Broken []string
}

// Len returns the number of refs.
func (l *List) Len() int { return len(l.ends) }

// Name returns ref i's full name.
func (l *List) Name(i int) string {
	start := uint32(0)
	if i > 0 {
		start = l.ends[i-1]
	}
	return string(l.names[start:l.ends[i]])
}

// Find returns the index of the ref named name, a full name, by binary
// search of the sorted names, allocating nothing.
func (l *List) Find(name string) (int, bool) {
	return sort.Find(len(l.ends), func(i int) int {
		start := uint32(0)
		if i > 0 {
			start = l.ends[i-1]
		}
		b := l.names[start:l.ends[i]]
		for j := range min(len(name), len(b)) {
			if name[j] != b[j] {
				return cmp.Compare(name[j], b[j])
			}
		}
		return cmp.Compare(len(name), len(b))
	})
}

// OID returns the object ref i points at, through symbolic refs. A
// symbolic ref whose target does not exist is not listed, as git leaves
// it out.
func (l *List) OID(i int) []byte {
	return l.oids[i*l.hashSize : (i+1)*l.hashSize : (i+1)*l.hashSize]
}

// Peeled returns the commit (or other object) an annotated tag points at,
// as packed-refs records it or as the caller resolved it; nil when not
// known.
func (l *List) Peeled(i int) []byte {
	o := l.peeled[i*l.hashSize : (i+1)*l.hashSize : (i+1)*l.hashSize]
	if isZero(o) {
		return nil
	}
	return o
}

// PeelKnown reports whether Peeled is known for ref i, so that nil from
// it means the ref names no annotated tag: packed-refs written with the
// fully-peeled trait (or peeled, for tags) and reftable records say so,
// a loose ref does not.
func (l *List) PeelKnown(i int) bool { return l.known[i/64]&(1<<(i%64)) != 0 }

// Symbolic returns the target of a symbolic ref, empty for a direct one.
func (l *List) Symbolic(i int) string { return l.targets[i] }

// Equal reports whether o holds the same refs with the same values, so a
// refresh that read nothing new can keep l and what was built from it.
func (l *List) Equal(o *List) bool {
	if l == o {
		return true
	}
	if l == nil || o == nil || l.hashSize != o.hashSize {
		return false
	}
	return bytes.Equal(l.names, o.names) && slices.Equal(l.ends, o.ends) &&
		bytes.Equal(l.oids, o.oids) && bytes.Equal(l.peeled, o.peeled) && slices.Equal(l.known, o.known) &&
		maps.Equal(l.targets, o.targets) && slices.Equal(l.Broken, o.Broken)
}

// Bytes returns the memory the list holds.
func (l *List) Bytes() int {
	return cap(l.names) + cap(l.ends)*4 + cap(l.oids) + cap(l.peeled) + cap(l.known)*8
}

func isZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

type raw struct {
	name   string
	oid    []byte
	peeled []byte
	// peelKnown: peeled is nil because the ref is no annotated tag.
	peelKnown bool
	symref    string
}

// List reads every ref under refs/.
func (s *Store) List() (*List, error) {
	packedPath := filepath.Join(s.commonDir, "packed-refs")
	var all map[string]raw
	var broken []string
	for attempt := 0; ; attempt++ {
		before, _ := os.Stat(packedPath)
		packed, err := s.readPacked(packedPath)
		if err != nil {
			return nil, err
		}
		all = packed
		broken = broken[:0]
		if err := s.walkLoose(s.commonDir, true, all, &broken); err != nil {
			return nil, err
		}
		if s.gitDir != s.commonDir {
			if err := s.walkLoose(s.gitDir, false, all, &broken); err != nil {
				return nil, err
			}
		}
		after, _ := os.Stat(packedPath)
		// git pack-refs moves loose refs into packed-refs and then deletes
		// them; reading the old packed-refs and then the pruned loose refs
		// would lose them, so read again if packed-refs changed meanwhile.
		if sameFile(before, after) || attempt == 3 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	return build(all, s.hashSize, broken, func(name string) []byte {
		oid, _ := s.resolveIn(all, name, 1)
		return oid
	}), nil
}

// build lays out the refs of all under refs/ in name order, resolving
// symbolic ones through resolve. A symbolic ref that resolves to nothing
// is left out, as git for-each-ref leaves it out.
func build(all map[string]raw, hashSize int, broken []string, resolve func(string) []byte) *List {
	names := make([]string, 0, len(all))
	for n := range all {
		if strings.HasPrefix(n, "refs/") {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	l := &List{hashSize: hashSize, targets: map[int]string{}, Broken: broken}
	l.oids = make([]byte, 0, len(names)*hashSize)
	l.peeled = make([]byte, 0, len(names)*hashSize)
	l.known = make([]uint64, (len(names)+63)/64)
	zero := make([]byte, hashSize)
	for _, n := range names {
		r := all[n]
		oid := r.oid
		if r.symref != "" {
			if oid = resolve(r.symref); oid == nil {
				continue
			}
			l.targets[len(l.ends)] = r.symref
		}
		l.names = append(l.names, n...)
		l.ends = append(l.ends, uint32(len(l.names)))
		l.oids = append(l.oids, oid...)
		p := r.peeled
		if p == nil {
			p = zero
		}
		l.peeled = append(l.peeled, p...)
		if r.symref == "" && (r.peelKnown || r.peeled != nil) {
			i := len(l.ends) - 1
			l.known[i/64] |= 1 << (i % 64)
		}
	}
	return l
}

func sameFile(a, b os.FileInfo) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Size() == b.Size() && a.ModTime().Equal(b.ModTime()) && os.SameFile(a, b)
}

func (s *Store) resolveIn(all map[string]raw, name string, depth int) ([]byte, error) {
	if depth > maxSymrefDepth {
		return nil, fmt.Errorf("refs: symbolic ref loop at %s", name)
	}
	r, ok := all[name]
	if !ok {
		return nil, nil
	}
	if r.symref != "" {
		return s.resolveIn(all, r.symref, depth+1)
	}
	return r.oid, nil
}

func (s *Store) readPacked(path string) (map[string]raw, error) {
	st, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]raw{}, nil
	}
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > maxPacked {
		return nil, fmt.Errorf("refs: packed-refs is not a regular file under %d bytes", maxPacked)
	}
	data, err := osfile.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return s.parsePacked(data), nil
}

// parsePacked reads packed-refs, skipping lines git would not accept.
func (s *Store) parsePacked(data []byte) map[string]raw {
	refs := map[string]raw{}
	last := ""
	// The header's traits say which refs have a peeled line when they
	// are annotated tags: all of them, or those under refs/tags/.
	var fully, tags bool
	if traits, ok := bytes.CutPrefix(data, []byte("# pack-refs with:")); ok {
		traits, _, _ = bytes.Cut(traits, []byte{'\n'})
		fully = bytes.Contains(traits, []byte(" fully-peeled "))
		tags = bytes.Contains(traits, []byte(" peeled "))
	}
	for len(data) > 0 {
		var line []byte
		line, data, _ = bytes.Cut(data, []byte{'\n'})
		line = bytes.TrimRight(line, "\r")
		switch {
		case len(line) == 0 || line[0] == '#':
			continue
		case line[0] == '^':
			if oid, ok := s.parseOID(line[1:]); ok && last != "" {
				r := refs[last]
				r.peeled = oid
				refs[last] = r
			}
			continue
		}
		hexID, name, ok := bytes.Cut(line, []byte{' '})
		oid, okID := s.parseOID(hexID)
		if !ok || !okID || !ValidName(string(name)) {
			last = ""
			continue
		}
		last = string(name)
		refs[last] = raw{name: last, oid: oid, peelKnown: fully || tags && strings.HasPrefix(last, "refs/tags/")}
	}
	return refs
}

func (s *Store) parseOID(b []byte) ([]byte, bool) {
	b = bytes.TrimSpace(b)
	if len(b) != 2*s.hashSize {
		return nil, false
	}
	oid := make([]byte, s.hashSize)
	if _, err := hex.Decode(oid, b); err != nil {
		return nil, false
	}
	return oid, true
}

// walkLoose reads loose refs under dir/refs, the ones that belong there:
// shared refs from the common directory, per-worktree refs from the git
// directory.
func (s *Store) walkLoose(dir string, common bool, all map[string]raw, broken *[]string) error {
	root := filepath.Join(dir, "refs")
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		name := filepath.ToSlash(rel)
		if (perWorktree(name) == common && s.gitDir != s.commonDir) || !ValidName(name) {
			return nil
		}
		r, err := s.readLoose(p, name)
		if errors.Is(err, fs.ErrNotExist) {
			// Gone since listed: git deleted it, or packed it, which the
			// packed-refs read again tells.
			return nil
		}
		if err != nil {
			// git skips a broken loose ref, and with it the packed value
			// it overrides.
			delete(all, name)
			*broken = append(*broken, name)
			return nil
		}
		if old, ok := all[name]; ok && r.symref == "" && bytes.Equal(old.oid, r.oid) {
			// packed-refs knows the peeled value
			r.peeled, r.peelKnown = old.peeled, old.peelKnown
		}
		all[name] = r
		return nil
	})
}

func (s *Store) readLoose(path, name string) (raw, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return raw{}, err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		t := filepath.ToSlash(target)
		if err != nil || !strings.HasPrefix(t, "refs/") || !ValidName(t) {
			return raw{}, fmt.Errorf("refs: %s is a symlink out of refs", name)
		}
		return raw{name: name, symref: t}, nil
	}
	// Only a small regular file is a ref: opening a FIFO would block, and
	// a huge file would be read whole.
	if !st.Mode().IsRegular() || st.Size() > maxLoose {
		return raw{}, fmt.Errorf("refs: %s is not a ref file", name)
	}
	f, err := osfile.Open(path)
	if err != nil {
		return raw{}, err
	}
	b, err := io.ReadAll(io.LimitReader(f, maxLoose))
	f.Close()
	if err != nil {
		return raw{}, err
	}
	b = bytes.TrimRight(b, "\r\n")
	if t, ok := bytes.CutPrefix(b, []byte("ref:")); ok {
		target := string(bytes.TrimSpace(t))
		if !ValidName(target) {
			return raw{}, fmt.Errorf("refs: %s points at %q", name, target)
		}
		return raw{name: name, symref: target}, nil
	}
	// git takes the object id at the start and ignores whatever follows
	// it after whitespace.
	n := 2 * s.hashSize
	if len(b) < n || len(b) > n && !isSpace(b[n]) || isSpace(b[0]) {
		return raw{}, fmt.Errorf("refs: %s does not hold an object id", name)
	}
	oid, ok := s.parseOID(b[:n])
	if !ok || isZero(oid) {
		// git counts the null id as broken too.
		return raw{}, fmt.Errorf("refs: %s does not hold an object id", name)
	}
	return raw{name: name, oid: oid}, nil
}

// isSpace is git's own isspace, which knows only these four.
func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// Head describes HEAD.
type Head struct {
	// Target is the branch HEAD names, "refs/heads/main"; empty when
	// detached.
	Target string
	// OID is the commit HEAD points at; nil when unborn.
	OID      []byte
	Detached bool
	// Unborn is a branch with no commit yet, as in a new repository.
	Unborn bool
}

// Head reads HEAD of the work tree the store was opened for.
func (s *Store) Head() (Head, error) {
	r, err := s.readLoose(filepath.Join(s.gitDir, "HEAD"), "HEAD")
	if err != nil {
		return Head{}, err
	}
	if r.symref == "" {
		return Head{OID: r.oid, Detached: true}, nil
	}
	h := Head{Target: r.symref}
	oid, err := s.Resolve(r.symref)
	if err != nil {
		return Head{}, err
	}
	h.OID, h.Unborn = oid, oid == nil
	return h, nil
}

// Resolve returns the object a ref points at, following symbolic refs, or
// nil when it does not exist.
func (s *Store) Resolve(name string) ([]byte, error) {
	for depth := 0; depth <= maxSymrefDepth; depth++ {
		path := filepath.Join(s.dirFor(name), filepath.FromSlash(name))
		r, err := s.readLoose(path, name)
		switch {
		case err == nil && r.symref != "":
			name = r.symref
			continue
		case err == nil:
			return r.oid, nil
		case !errors.Is(err, fs.ErrNotExist):
			var pe *fs.PathError
			if !errors.As(err, &pe) {
				return nil, err
			}
		}
		packed, perr := s.readPacked(filepath.Join(s.commonDir, "packed-refs"))
		if perr != nil {
			return nil, perr
		}
		if p, ok := packed[name]; ok {
			return p.oid, nil
		}
		return nil, nil
	}
	return nil, fmt.Errorf("refs: symbolic ref loop at %s", name)
}

// ValidName applies git check-ref-format's rules to a full ref name.
func ValidName(name string) bool {
	if name == "" || len(name) > maxName || name == "@" || strings.HasSuffix(name, "/") || strings.HasPrefix(name, "/") ||
		strings.HasSuffix(name, ".") || strings.Contains(name, "..") || strings.Contains(name, "@{") ||
		strings.Contains(name, "//") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part[0] == '.' || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c < 0x20 || c == 0x7f || strings.IndexByte(" ~^:?*[\\", c) >= 0 {
			return false
		}
	}
	return true
}
