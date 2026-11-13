package refs

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Reftable limits: a table larger than maxTable or a name longer than
// maxName is refused rather than read; git writes neither.
const (
	maxTable = 1 << 30
	maxName  = 4096
)

// ErrReftable is returned for a table that is not well formed.
var ErrReftable = errors.New("refs: malformed reftable")

// Reader is what both ref formats give.
type Reader interface {
	List() (*List, error)
	Head() (Head, error)
	Resolve(name string) ([]byte, error)
}

var _ Reader = (*Store)(nil)
var _ Reader = (*Reftable)(nil)

// Reftable reads refs stored as reftable stacks: the repository's in
// commonDir/reftable, and a linked worktree's own (HEAD, refs/bisect,
// refs/worktree) in gitDir/reftable. Only ref blocks are read: no object
// index and no logs. Tables never change once written, so they are kept
// between reads by name. Safe for use by several goroutines.
type Reftable struct {
	gitDir, commonDir string
	hashSize          int
	mu                sync.Mutex
	tables            map[string][]byte // by path
}

// NewReftable returns a reader for a repository using reftable.
func NewReftable(gitDir, commonDir string, hashSize int) *Reftable {
	return &Reftable{gitDir: gitDir, commonDir: commonDir, hashSize: hashSize, tables: map[string][]byte{}}
}

type table struct {
	data     []byte
	hdr      int // 24 for version 1, 28 for version 2
	block    int // block size; 0 when unaligned
	hashSize int
	refEnd   int // end of the ref blocks; 0 when there are none
}

func openTable(data []byte, hashSize int) (*table, error) {
	if len(data) < 24+68 || string(data[:4]) != "REFT" {
		return nil, ErrReftable
	}
	t := &table{data: data, hdr: 24, hashSize: 20}
	foot := 68
	switch data[4] {
	case 1:
	case 2:
		t.hdr, foot = 28, 72
		if len(data) < 28+72 {
			return nil, ErrReftable
		}
		switch string(data[24:28]) {
		case "sha1":
		case "s256":
			t.hashSize = 32
		default:
			return nil, fmt.Errorf("%w: hash %q", ErrReftable, data[24:28])
		}
	default:
		return nil, fmt.Errorf("%w: version %d", ErrReftable, data[4])
	}
	if t.hashSize != hashSize {
		return nil, fmt.Errorf("%w: hash size %d in a repository of %d", ErrReftable, t.hashSize, hashSize)
	}
	f := data[len(data)-foot:]
	if !bytes.Equal(f[:t.hdr], data[:t.hdr]) || crc32.ChecksumIEEE(f[:foot-4]) != binary.BigEndian.Uint32(f[foot-4:]) {
		return nil, fmt.Errorf("%w: footer", ErrReftable)
	}
	t.block = int(data[5])<<16 | int(data[6])<<8 | int(data[7])
	body := uint64(len(data) - foot)
	// The ref blocks end where the first other section starts: the ref
	// index, objects (whose offset carries the id length in its low 5
	// bits), the object index, logs, the log index.
	end := body
	for i, shift := range []uint{0, 5, 0, 0, 0} {
		if p := binary.BigEndian.Uint64(f[t.hdr+8*i:]) >> shift; p != 0 && p < end {
			end = p
		}
	}
	if int(end) > t.hdr && data[t.hdr] == 'r' {
		t.refEnd = int(end)
	}
	return t, nil
}

func u24(b []byte) int { return int(b[0])<<16 | int(b[1])<<8 | int(b[2]) }

// refBlock bounds the ref block at off once: its records and the offset
// of the next block.
func (t *table) refBlock(off int) (recs []byte, start, next int, err error) {
	p := off
	if off == 0 {
		p = t.hdr // the first block shares its space with the header
	}
	if p+4 > t.refEnd || t.data[p] != 'r' {
		return nil, 0, 0, ErrReftable
	}
	n := u24(t.data[p+1:])
	if n < p-off+4+2 || off+n > t.refEnd {
		return nil, 0, 0, ErrReftable
	}
	b := t.data[off : off+n]
	restarts := int(binary.BigEndian.Uint16(b[n-2:]))
	recEnd := n - 2 - 3*restarts
	if recEnd < p-off+4 {
		return nil, 0, 0, ErrReftable
	}
	next = off + n
	// An aligned table pads a block to the block size with zeros.
	if t.block > 0 && n < t.block && (next >= t.refEnd || t.data[next] == 0) {
		next = off + t.block
	}
	return b[:recEnd], p - off + 4, next, nil
}

func varint(b []byte, p int) (uint64, int, bool) {
	if p >= len(b) {
		return 0, 0, false
	}
	v := uint64(b[p] & 0x7f)
	for b[p]&0x80 != 0 {
		p++
		if p >= len(b) || v >= 1<<56 {
			return 0, 0, false
		}
		v = (v+1)<<7 | uint64(b[p]&0x7f)
	}
	return v, p + 1, true
}

// Value types of a ref record.
const (
	deletion = 0
	direct   = 1
	peeled   = 2
	symbolic = 3
)

// cursor walks one table's ref records in name order.
type cursor struct {
	t     *table
	recs  []byte
	p     int
	next  int
	key   []byte
	typ   byte
	val   []byte
	done  bool
	depth int // position in the stack: higher is newer
}

func (c *cursor) open(t *table, depth int) error {
	c.t, c.depth = t, depth
	if t.refEnd == 0 {
		c.done = true
		return nil
	}
	var err error
	if c.recs, c.p, c.next, err = t.refBlock(0); err != nil {
		return err
	}
	return c.advance()
}

func (c *cursor) advance() error {
	for c.p >= len(c.recs) {
		if c.next >= c.t.refEnd || c.t.data[c.next] != 'r' {
			c.done = true
			return nil
		}
		var err error
		if c.recs, c.p, c.next, err = c.t.refBlock(c.next); err != nil {
			return err
		}
		c.key = c.key[:0] // a block's first record has no prefix
	}
	b, p := c.recs, c.p
	prefix, p, ok1 := varint(b, p)
	suffix, p, ok2 := varint(b, p)
	typ := byte(suffix & 7)
	suffix >>= 3
	// Capping the name stops a crafted block from making every record
	// repeat a block-long prefix.
	if !ok1 || !ok2 || prefix > uint64(len(c.key)) || suffix > uint64(len(b)-p) || prefix+suffix > maxName {
		return ErrReftable
	}
	c.key = append(c.key[:prefix], b[p:p+int(suffix)]...)
	p += int(suffix)
	if _, p, ok1 = varint(b, p); !ok1 { // update index delta
		return ErrReftable
	}
	n := 0
	switch typ {
	case deletion:
	case direct:
		n = c.t.hashSize
	case peeled:
		n = 2 * c.t.hashSize
	case symbolic:
		l, q, ok := varint(b, p)
		if !ok || l > uint64(len(b)-q) {
			return ErrReftable
		}
		n, p = int(l), q
	default:
		return ErrReftable
	}
	if n > len(b)-p {
		return ErrReftable
	}
	c.typ, c.val, c.p = typ, b[p:p+n], p+n
	return nil
}

type record struct {
	typ byte
	val []byte
}

// merge calls emit with every name of the stack in order and the newest
// table's record for it; a deletion in a newer table hides older records
// and is passed on as such.
func merge(tabs []*table, emit func(name []byte, r record)) error {
	cs := make([]cursor, len(tabs))
	for i, t := range tabs {
		if err := cs[i].open(t, i); err != nil {
			return err
		}
	}
	var name []byte
	for {
		best := -1
		for i := range cs {
			if cs[i].done {
				continue
			}
			if best < 0 || bytes.Compare(cs[i].key, cs[best].key) < 0 ||
				(bytes.Equal(cs[i].key, cs[best].key) && cs[i].depth > cs[best].depth) {
				best = i
			}
		}
		if best < 0 {
			return nil
		}
		name = append(name[:0], cs[best].key...)
		emit(name, record{cs[best].typ, cs[best].val})
		for i := range cs {
			if !cs[i].done && bytes.Equal(cs[i].key, name) {
				if err := cs[i].advance(); err != nil {
					return err
				}
			}
		}
	}
}

// stack reads dir/tables.list and the tables it names. When git compacts
// the stack meanwhile, a table is gone before the list says so; the list
// is read again, as git's readers do.
func (r *Reftable) stack(dir string) ([]*table, error) {
	var err error
	for attempt := 0; attempt < 20; attempt++ {
		var list []byte
		list, err = os.ReadFile(filepath.Join(dir, "tables.list"))
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		var tabs []*table
		names := strings.Fields(string(list))
		for _, name := range names {
			if strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
				return nil, fmt.Errorf("%w: table name %q", ErrReftable, name)
			}
			var data []byte
			if data, err = r.read(filepath.Join(dir, name)); err != nil {
				break
			}
			var t *table
			if t, err = openTable(data, r.hashSize); err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			tabs = append(tabs, t)
		}
		if err == nil {
			r.forget(dir, names)
			return tabs, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		time.Sleep(time.Duration(attempt+1) * time.Millisecond)
	}
	return nil, err
}

func (r *Reftable) read(path string) ([]byte, error) {
	r.mu.Lock()
	data, ok := r.tables[path]
	r.mu.Unlock()
	if ok {
		return data, nil
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Size() > maxTable {
		return nil, fmt.Errorf("%w: %s is %d bytes", ErrReftable, path, st.Size())
	}
	if data, err = os.ReadFile(path); err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.tables[path] = data
	r.mu.Unlock()
	return data, nil
}

// forget drops tables of dir that the list no longer names.
func (r *Reftable) forget(dir string, names []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for path := range r.tables {
		if filepath.Dir(path) == dir && !slices.Contains(names, filepath.Base(path)) {
			delete(r.tables, path)
		}
	}
}

// records reads every live record of the repository's stacks: the common
// one for shared refs and, in a linked worktree, its own for the rest.
func (r *Reftable) records(fn func(name []byte, rec record)) error {
	common, err := r.stack(filepath.Join(r.commonDir, "reftable"))
	if err != nil {
		return err
	}
	linked := r.gitDir != r.commonDir
	var own []*table
	if linked {
		if own, err = r.stack(filepath.Join(r.gitDir, "reftable")); err != nil {
			return err
		}
	}
	err = merge(common, func(name []byte, rec record) {
		if !linked || !perWorktree(string(name)) {
			fn(name, rec)
		}
	})
	if err != nil || !linked {
		return err
	}
	return merge(own, func(name []byte, rec record) {
		if perWorktree(string(name)) {
			fn(name, rec)
		}
	})
}

// List reads every ref under refs/.
func (r *Reftable) List() (*List, error) {
	all := map[string]raw{}
	hs := r.hashSize
	err := r.records(func(name []byte, rec record) {
		n := string(name)
		switch rec.typ {
		case direct:
			all[n] = raw{name: n, oid: bytes.Clone(rec.val)}
		case peeled:
			all[n] = raw{name: n, oid: bytes.Clone(rec.val[:hs]), peeled: bytes.Clone(rec.val[hs:])}
		case symbolic:
			all[n] = raw{name: n, symref: string(rec.val)}
		}
	})
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(all))
	for n := range all {
		if strings.HasPrefix(n, "refs/") {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	l := &List{hashSize: hs, targets: map[int]string{}}
	zero := make([]byte, hs)
	for i, n := range names {
		rr := all[n]
		l.names = append(l.names, n...)
		l.ends = append(l.ends, uint32(len(l.names)))
		oid := rr.oid
		if rr.symref != "" {
			l.targets[i] = rr.symref
			oid = resolveIn(all, rr.symref)
		}
		if oid == nil {
			oid = zero
		}
		l.oids = append(l.oids, oid...)
		p := rr.peeled
		if p == nil {
			p = zero
		}
		l.peeled = append(l.peeled, p...)
	}
	return l, nil
}

func resolveIn(all map[string]raw, name string) []byte {
	for depth := 0; depth <= maxSymrefDepth; depth++ {
		rr, ok := all[name]
		if !ok {
			return nil
		}
		if rr.symref == "" {
			return rr.oid
		}
		name = rr.symref
	}
	return nil
}

// Resolve returns the object a ref points at, following symbolic refs, or
// nil when it does not exist.
func (r *Reftable) Resolve(name string) ([]byte, error) {
	all := map[string]raw{}
	err := r.records(func(n []byte, rec record) {
		switch rec.typ {
		case direct, peeled:
			all[string(n)] = raw{oid: bytes.Clone(rec.val[:r.hashSize])}
		case symbolic:
			all[string(n)] = raw{symref: string(rec.val)}
		}
	})
	if err != nil {
		return nil, err
	}
	return resolveIn(all, name), nil
}

// Head reads HEAD of the work tree the reader was opened for. With
// reftable, .git/HEAD is a stub for older gits and the real HEAD is a
// record of the stack.
func (r *Reftable) Head() (Head, error) {
	var head *raw
	all := map[string]raw{}
	err := r.records(func(n []byte, rec record) {
		var rr raw
		switch rec.typ {
		case direct, peeled:
			rr.oid = bytes.Clone(rec.val[:r.hashSize])
		case symbolic:
			rr.symref = string(rec.val)
		default:
			return
		}
		all[string(n)] = rr
		if string(n) == "HEAD" {
			head = &rr
		}
	})
	if err != nil {
		return Head{}, err
	}
	switch {
	case head == nil:
		return Head{}, fmt.Errorf("%w: no HEAD", ErrReftable)
	case head.symref == "":
		return Head{OID: head.oid, Detached: true}, nil
	}
	oid := resolveIn(all, head.symref)
	return Head{Target: head.symref, OID: oid, Unborn: oid == nil}, nil
}
