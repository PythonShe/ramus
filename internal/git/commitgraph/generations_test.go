package commitgraph

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// fakeCommit is a commit of a graph file the test writes itself, its
// generation data as given, whatever git would compute.
type fakeCommit struct {
	time   int64
	parent int // index of the parent in the list, -1 for none
	topo   uint32
	offset uint64 // corrected commit date less time
}

// writeGraph writes a single-file commit-graph of cs, with corrected
// commit dates, under objects/info, and returns the objects folder. The
// commits' ids are their index in the first byte, so they sort as listed.
func writeGraph(t *testing.T, cs []fakeCommit) string {
	t.Helper()
	const hash = 20
	fanout := make([]byte, 256*4)
	for b := 0; b < 256; b++ {
		c := 0
		for i := range cs {
			if i <= b {
				c++
			}
		}
		binary.BigEndian.PutUint32(fanout[b*4:], uint32(c))
	}
	var oids, cdat, gda, gdo []byte
	for i, c := range cs {
		oid := make([]byte, hash)
		oid[0] = byte(i)
		oids = append(oids, oid...)
		rec := make([]byte, hash+16)
		rec[0] = 0xee // the tree
		p := uint32(parentNone)
		if c.parent >= 0 {
			p = uint32(c.parent)
		}
		binary.BigEndian.PutUint32(rec[hash:], p)
		binary.BigEndian.PutUint32(rec[hash+4:], parentNone)
		binary.BigEndian.PutUint32(rec[hash+8:], c.topo<<generationShift|uint32(c.time>>32)&3)
		binary.BigEndian.PutUint32(rec[hash+12:], uint32(c.time))
		cdat = append(cdat, rec...)
		off := uint32(c.offset)
		if c.offset >= gdaOverflow {
			off = gdaOverflow | uint32(len(gdo)/8)
			gdo = binary.BigEndian.AppendUint64(gdo, c.offset)
		}
		gda = binary.BigEndian.AppendUint32(gda, off)
	}
	type chunk struct {
		id   string
		data []byte
	}
	chunks := []chunk{{"OIDF", fanout}, {"OIDL", oids}, {"CDAT", cdat}, {"GDA2", gda}}
	if len(gdo) > 0 {
		chunks = append(chunks, chunk{"GDO2", gdo})
	}
	b := []byte{'C', 'G', 'P', 'H', 1, 1, byte(len(chunks)), 0}
	off := uint64(8 + (len(chunks)+1)*12)
	for _, c := range chunks {
		b = append(b, c.id...)
		b = binary.BigEndian.AppendUint64(b, off)
		off += uint64(len(c.data))
	}
	b = append(b, 0, 0, 0, 0)
	b = binary.BigEndian.AppendUint64(b, off)
	for _, c := range chunks {
		b = append(b, c.data...)
	}
	b = append(b, make([]byte, hash)...) // the checksum, not read
	objects := t.TempDir()
	os.MkdirAll(filepath.Join(objects, "info"), 0o755)
	if err := os.WriteFile(filepath.Join(objects, "info", "commit-graph"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return objects
}

// A chain under a commit dated past 2106 (2^32 + 5): its child dated
// 100, and a grandchild dated 200.
const future = 1<<32 + 5

// git from 2.41 to 2.54 kept a parent's corrected commit date in 32 bits
// while computing its children's, and so wrote, below a commit dated past
// 2106, dates that go backwards: offset 0 for the child and grandchild.
// Pruned by those, a walk would stop at the child and miss the commit
// above it; the graph answers with topological levels instead.
func TestCorrectedDatesThatGoBackwards(t *testing.T) {
	g, err := Open(writeGraph(t, []fakeCommit{
		{time: future, parent: -1, topo: 1, offset: 0},
		{time: 100, parent: 0, topo: 2, offset: 0},
		{time: 200, parent: 1, topo: 3, offset: 0},
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if g.HasGenerationV2() {
		t.Fatal("corrected dates that go backwards are used")
	}
	for pos, want := range []uint64{1, 2, 3} {
		if got := g.Generation(uint32(pos)); got != want {
			t.Errorf("generation of %d = %d, want its topological level %d", pos, got, want)
		}
	}
}

// The same history as git 2.55 and later write it: the child's and the
// grandchild's corrected dates follow the future commit's, through the
// overflow chunk, and are used.
func TestCorrectedDatesPastTheOffsetLimit(t *testing.T) {
	g, err := Open(writeGraph(t, []fakeCommit{
		{time: future, parent: -1, topo: 1, offset: 0},
		{time: 100, parent: 0, topo: 2, offset: future + 1 - 100},
		{time: 200, parent: 1, topo: 3, offset: future + 2 - 200},
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if !g.HasGenerationV2() {
		t.Fatal("sound corrected dates are not used")
	}
	for pos, want := range []uint64{future, future + 1, future + 2} {
		if got := g.Generation(uint32(pos)); got != want {
			t.Errorf("generation of %d = %d, want %d", pos, got, want)
		}
	}
}
