// Package commitgraph reads git's commit-graph files: a single
// objects/info/commit-graph file or a split chain under
// objects/info/commit-graphs, with generation data in both versions,
// changed-path Bloom filters in both versions, and octopus edges.
//
// The files are memory-mapped and read in place; a Graph holds no
// per-commit objects. Commits are addressed by their position in the
// combined graph, base layers first, as git numbers them.
package commitgraph

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"ramus/internal/git/mmap"
)

// None is the position returned for a commit that is not in the graph.
const None = ^uint32(0)

// Limits that keep a damaged or hostile file from making the reader
// allocate or loop without bound. Every size read from a header is also
// checked against the length of the file.
const (
	maxChunks = 64
	maxLayers = 1024
	// git itself stores positions in 31 bits.
	maxCommits = 1<<31 - 1
	// MaxParents bounds one commit's parents, as in the commit store.
	MaxParents = 1 << 15
)

const (
	parentNone      = 0x70000000
	parentOctopus   = 0x80000000
	edgeLast        = 0x80000000
	generationShift = 2
	gdaOverflow     = 0x80000000
)

var (
	chunkFanout    = chunkID("OIDF")
	chunkLookup    = chunkID("OIDL")
	chunkData      = chunkID("CDAT")
	chunkGenData   = chunkID("GDA2")
	chunkGenOver   = chunkID("GDO2")
	chunkEdges     = chunkID("EDGE")
	chunkBloomIdx  = chunkID("BIDX")
	chunkBloomData = chunkID("BDAT")
	chunkBase      = chunkID("BASE")
)

func chunkID(s string) uint32 { return binary.BigEndian.Uint32([]byte(s)) }

// ErrNotFound means the repository has no commit-graph.
var ErrNotFound = errors.New("commitgraph: no commit-graph")

// FormatError reports a commit-graph file that cannot be read.
type FormatError struct {
	Path   string
	Reason string
}

func (e *FormatError) Error() string { return "commitgraph: " + e.Path + ": " + e.Reason }

// Graph is an open commit-graph, one or more layers.
type Graph struct {
	layers   []*layer
	hashSize int
	n        uint32
	// genV2 is true when every layer has corrected commit dates; git uses
	// them only then.
	genV2 bool
	// bloom is the Bloom filter settings shared by every layer that has
	// filters; zero when no layer has them or the layers disagree.
	bloom BloomSettings
}

type layer struct {
	path     string
	file     *mmap.File
	hashSize int
	base     uint32 // commits in the layers below this one
	n        uint32
	fanout   []byte
	oids     []byte
	cdat     []byte
	gda2     []byte
	gdo2     []byte
	edges    []byte
	bidx     []byte
	bdat     []byte // filters, after the settings header
	bloom    BloomSettings
	baseOIDs []byte
}

// Open reads the commit-graph of the repository whose objects directory is
// objectsDir. A split chain is preferred over a single file, as git does.
// It returns ErrNotFound when there is neither.
func Open(objectsDir string) (*Graph, error) {
	chain := filepath.Join(objectsDir, "info", "commit-graphs", "commit-graph-chain")
	hashes, err := readChain(chain)
	switch {
	case err == nil:
		paths := make([]string, len(hashes))
		for i, h := range hashes {
			paths[i] = filepath.Join(objectsDir, "info", "commit-graphs", "graph-"+h+".graph")
		}
		return openLayers(paths)
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}
	single := filepath.Join(objectsDir, "info", "commit-graph")
	if _, err := os.Stat(single); errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	return openLayers([]string{single})
}

func readChain(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var hashes []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		h := strings.TrimSpace(sc.Text())
		if h == "" {
			continue
		}
		if _, err := hex.DecodeString(h); err != nil || (len(h) != 40 && len(h) != 64) {
			return nil, &FormatError{path, fmt.Sprintf("bad layer hash %q", h)}
		}
		hashes = append(hashes, h)
		if len(hashes) > maxLayers {
			return nil, &FormatError{path, "too many layers"}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(hashes) == 0 {
		return nil, &FormatError{path, "empty chain"}
	}
	return hashes, nil
}

func openLayers(paths []string) (_ *Graph, err error) {
	g := &Graph{genV2: true}
	defer func() {
		if err != nil {
			g.Close()
		}
	}()
	for i, p := range paths {
		l, err := openLayer(p, g.n)
		if err != nil {
			return nil, err
		}
		g.layers = append(g.layers, l)
		if i == 0 {
			g.hashSize = l.hashSize
		} else if l.hashSize != g.hashSize {
			return nil, &FormatError{p, "layers use different hash functions"}
		}
		if len(l.baseOIDs) != i*g.hashSize {
			return nil, &FormatError{p, fmt.Sprintf("names %d base layers, chain has %d", len(l.baseOIDs)/g.hashSize, i)}
		}
		for j := 0; j < i; j++ {
			want, _ := hex.DecodeString(filepath.Base(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(paths[j]), "graph-"), ".graph")))
			if !bytes.Equal(l.baseOIDs[j*g.hashSize:(j+1)*g.hashSize], want) {
				return nil, &FormatError{p, fmt.Sprintf("base layer %d does not match the chain", j)}
			}
		}
		if uint64(g.n)+uint64(l.n) > maxCommits {
			return nil, &FormatError{p, "too many commits"}
		}
		g.n += l.n
		if l.gda2 == nil {
			g.genV2 = false
		}
	}
	g.bloom = g.layers[0].bloom
	for _, l := range g.layers[1:] {
		if l.bloom != g.bloom {
			g.bloom = BloomSettings{}
		}
	}
	return g, nil
}

func openLayer(path string, base uint32) (*layer, error) {
	f, err := mmap.Open(path)
	if err != nil {
		return nil, err
	}
	l := &layer{path: path, file: f, base: base}
	if err := l.parse(); err != nil {
		f.Close()
		return nil, err
	}
	return l, nil
}

func (l *layer) bad(format string, args ...any) error {
	return &FormatError{l.path, fmt.Sprintf(format, args...)}
}

func (l *layer) parse() error {
	b := l.file.Bytes()
	const header = 8
	if len(b) < header {
		return l.bad("file too short")
	}
	if string(b[:4]) != "CGPH" {
		return l.bad("bad signature")
	}
	if b[4] != 1 {
		return l.bad("unsupported version %d", b[4])
	}
	var hashSize int
	switch b[5] {
	case 1:
		hashSize = 20
	case 2:
		hashSize = 32
	default:
		return l.bad("unknown hash version %d", b[5])
	}
	nChunks := int(b[6])
	nBases := int(b[7])
	if nChunks > maxChunks {
		return l.bad("%d chunks", nChunks)
	}
	tableEnd := header + (nChunks+1)*12
	if len(b) < tableEnd+hashSize {
		return l.bad("file too short for its chunk table")
	}
	end := uint64(len(b) - hashSize) // the trailing checksum
	chunks := map[uint32][]byte{}
	for i := 0; i < nChunks; i++ {
		e := b[header+i*12:]
		id := binary.BigEndian.Uint32(e)
		off := binary.BigEndian.Uint64(e[4:])
		next := binary.BigEndian.Uint64(e[16:])
		if id == 0 {
			return l.bad("chunk table ends early")
		}
		if off < uint64(tableEnd) || next < off || next > end {
			return l.bad("chunk %08x out of bounds", id)
		}
		if _, dup := chunks[id]; dup {
			return l.bad("duplicate chunk %08x", id)
		}
		chunks[id] = b[off:next]
	}
	if id := binary.BigEndian.Uint32(b[header+nChunks*12:]); id != 0 {
		return l.bad("chunk table not terminated")
	}

	l.fanout = chunks[chunkFanout]
	if len(l.fanout) != 256*4 {
		return l.bad("fanout chunk is %d bytes", len(l.fanout))
	}
	prev := uint32(0)
	for i := 0; i < 256; i++ {
		v := binary.BigEndian.Uint32(l.fanout[i*4:])
		if v < prev {
			return l.bad("fanout decreases at %d", i)
		}
		prev = v
	}
	l.n = prev
	if l.n > maxCommits {
		return l.bad("too many commits")
	}
	n := uint64(l.n)
	l.oids = chunks[chunkLookup]
	if uint64(len(l.oids)) != n*uint64(hashSize) {
		return l.bad("lookup chunk is %d bytes for %d commits", len(l.oids), n)
	}
	l.cdat = chunks[chunkData]
	if uint64(len(l.cdat)) != n*uint64(hashSize+16) {
		return l.bad("commit data chunk is %d bytes for %d commits", len(l.cdat), n)
	}
	if gda, ok := chunks[chunkGenData]; ok {
		if uint64(len(gda)) != n*4 {
			return l.bad("generation data chunk is %d bytes", len(gda))
		}
		l.gda2 = gda
		l.gdo2 = chunks[chunkGenOver]
		if len(l.gdo2)%8 != 0 {
			return l.bad("generation overflow chunk is %d bytes", len(l.gdo2))
		}
	}
	l.edges = chunks[chunkEdges]
	if len(l.edges)%4 != 0 {
		return l.bad("edge chunk is %d bytes", len(l.edges))
	}
	if err := l.parseBloom(chunks[chunkBloomIdx], chunks[chunkBloomData]); err != nil {
		return err
	}
	l.baseOIDs = chunks[chunkBase]
	if len(l.baseOIDs) != nBases*hashSize {
		return l.bad("base chunk is %d bytes for %d bases", len(l.baseOIDs), nBases)
	}
	l.hashSize = hashSize
	return nil
}

func (l *layer) parseBloom(idx, data []byte) error {
	if idx == nil || data == nil {
		return nil
	}
	if uint64(len(idx)) != uint64(l.n)*4 {
		// git ignores filters it cannot use; so do we.
		return nil
	}
	if len(data) < 12 {
		return nil
	}
	s := BloomSettings{
		Version:         binary.BigEndian.Uint32(data),
		NumHashes:       binary.BigEndian.Uint32(data[4:]),
		BitsPerEntry:    binary.BigEndian.Uint32(data[8:]),
		MaxChangedPaths: 512,
	}
	if (s.Version != 1 && s.Version != 2) || s.NumHashes == 0 || s.NumHashes > 64 {
		return nil
	}
	filters := data[12:]
	prev := uint32(0)
	for i := uint32(0); i < l.n; i++ {
		v := binary.BigEndian.Uint32(idx[int(i)*4:])
		if v < prev || int64(v) > int64(len(filters)) {
			return nil
		}
		prev = v
	}
	l.bidx, l.bdat, l.bloom = idx, filters, s
	return nil
}

// Close unmaps every layer. The Graph must not be used afterwards.
func (g *Graph) Close() error {
	var first error
	for _, l := range g.layers {
		if err := l.file.Close(); err != nil && first == nil {
			first = err
		}
	}
	g.layers = nil
	return first
}

// Len returns the number of commits in the graph.
func (g *Graph) Len() int { return int(g.n) }

// HashSize returns the width of an object id in bytes: 20 for SHA-1, 32 for
// SHA-256.
func (g *Graph) HashSize() int { return g.hashSize }

// Layers returns the number of files the graph was read from.
func (g *Graph) Layers() int { return len(g.layers) }

// HasGenerationV2 reports whether corrected commit dates are available.
func (g *Graph) HasGenerationV2() bool { return g.genV2 }

// Bloom returns the changed-path Bloom filter settings, with Version 0 when
// the graph has no usable filters.
func (g *Graph) Bloom() BloomSettings { return g.bloom }

func (g *Graph) layerOf(pos uint32) (*layer, uint32) {
	// Few layers in practice; the top one holds the newest commits.
	for i := len(g.layers) - 1; i >= 0; i-- {
		l := g.layers[i]
		if pos >= l.base {
			return l, pos - l.base
		}
	}
	panic("commitgraph: position out of range")
}

func (g *Graph) check(pos uint32) {
	if pos >= g.n {
		panic(fmt.Sprintf("commitgraph: position %d of %d", pos, g.n))
	}
}

// OID returns the object id of the commit at pos. The bytes point into the
// mapped file and must not be kept past Close.
func (g *Graph) OID(pos uint32) []byte {
	g.check(pos)
	l, i := g.layerOf(pos)
	// Offsets are computed in int: in uint32 they wrap on a large graph.
	o := int(i) * g.hashSize
	return l.oids[o : o+g.hashSize : o+g.hashSize]
}

// Lookup returns the position of the commit with object id oid, or None.
func (g *Graph) Lookup(oid []byte) uint32 {
	if len(oid) != g.hashSize {
		return None
	}
	for _, l := range g.layers {
		if i, ok := l.lookup(oid, g.hashSize); ok {
			return l.base + i
		}
	}
	return None
}

func (l *layer) lookup(oid []byte, h int) (uint32, bool) {
	first := oid[0]
	lo := uint32(0)
	if first > 0 {
		lo = binary.BigEndian.Uint32(l.fanout[(int(first)-1)*4:])
	}
	hi := binary.BigEndian.Uint32(l.fanout[int(first)*4:])
	for lo < hi {
		mid := lo + (hi-lo)/2
		switch c := bytes.Compare(l.oids[int(mid)*h:int(mid+1)*h], oid); {
		case c == 0:
			return mid, true
		case c < 0:
			lo = mid + 1
		default:
			hi = mid
		}
	}
	return 0, false
}

func (g *Graph) cdat(pos uint32) (*layer, uint32, []byte) {
	g.check(pos)
	l, i := g.layerOf(pos)
	w := g.hashSize + 16
	o := int(i) * w
	return l, i, l.cdat[o : o+w]
}

// Tree returns the object id of the commit's root tree.
func (g *Graph) Tree(pos uint32) []byte {
	_, _, d := g.cdat(pos)
	return d[:g.hashSize:g.hashSize]
}

// CommitTime returns the committer time, in seconds since the epoch.
func (g *Graph) CommitTime(pos uint32) int64 {
	_, _, d := g.cdat(pos)
	d = d[g.hashSize+8:]
	return int64(binary.BigEndian.Uint32(d)&3)<<32 | int64(binary.BigEndian.Uint32(d[4:]))
}

// Generation returns the commit's generation number: its corrected commit
// date when the graph has version 2 generation data, else its topological
// level. Either way a commit's generation is greater than its parents'.
func (g *Graph) Generation(pos uint32) uint64 {
	l, i, d := g.cdat(pos)
	if !g.genV2 {
		return uint64(binary.BigEndian.Uint32(d[g.hashSize+8:]) >> generationShift)
	}
	off := uint64(binary.BigEndian.Uint32(l.gda2[int(i)*4:]))
	if off&gdaOverflow != 0 {
		j := off &^ gdaOverflow
		if j*8+8 > uint64(len(l.gdo2)) {
			// A damaged file; fall back to something monotone enough to
			// keep a walk going rather than panic.
			return uint64(g.CommitTime(pos))
		}
		off = binary.BigEndian.Uint64(l.gdo2[j*8:])
	}
	return uint64(g.CommitTime(pos)) + off
}

// TopoLevel returns the commit's topological level, which every graph has.
func (g *Graph) TopoLevel(pos uint32) uint32 {
	_, _, d := g.cdat(pos)
	return binary.BigEndian.Uint32(d[g.hashSize+8:]) >> generationShift
}

// Parents appends the positions of the commit's parents to dst, in order,
// and returns it. A parent position that falls outside the graph means a
// damaged file; Parents reports it as an error rather than panic.
func (g *Graph) Parents(pos uint32, dst []uint32) ([]uint32, error) {
	l, _, d := g.cdat(pos)
	d = d[g.hashSize:]
	p1 := binary.BigEndian.Uint32(d)
	p2 := binary.BigEndian.Uint32(d[4:])
	if p1 == parentNone {
		return dst, nil
	}
	if p1 >= g.n {
		return dst, l.bad("parent position %d out of range", p1)
	}
	dst = append(dst, p1)
	switch {
	case p2 == parentNone:
		return dst, nil
	case p2&parentOctopus == 0:
		if p2 >= g.n {
			return dst, l.bad("parent position %d out of range", p2)
		}
		return append(dst, p2), nil
	}
	e := int64(p2 &^ parentOctopus)
	for n := 2; ; n++ {
		if e*4+4 > int64(len(l.edges)) {
			return dst, l.bad("edge list runs past its chunk")
		}
		if n > MaxParents {
			return dst, l.bad("more than %d parents", MaxParents)
		}
		v := binary.BigEndian.Uint32(l.edges[e*4:])
		p := v &^ edgeLast
		if p >= g.n {
			return dst, l.bad("parent position %d out of range", p)
		}
		dst = append(dst, p)
		if v&edgeLast != 0 {
			return dst, nil
		}
		e++
	}
}

// NumParents returns how many parents the commit has without collecting
// them, except for octopus merges where it walks the edge list.
func (g *Graph) NumParents(pos uint32) int {
	_, _, d := g.cdat(pos)
	d = d[g.hashSize:]
	p1 := binary.BigEndian.Uint32(d)
	p2 := binary.BigEndian.Uint32(d[4:])
	switch {
	case p1 == parentNone:
		return 0
	case p2 == parentNone:
		return 1
	case p2&parentOctopus == 0:
		return 2
	}
	var buf [8]uint32
	ps, _ := g.Parents(pos, buf[:0])
	return len(ps)
}
