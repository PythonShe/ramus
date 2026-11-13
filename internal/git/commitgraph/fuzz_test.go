package commitgraph

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzOpen feeds arbitrary bytes as a commit-graph file. Opening may fail;
// whatever opens must answer every accessor without panicking.
func FuzzOpen(f *testing.F) {
	seeds, _ := filepath.Glob("testdata/fuzz-seed-*.graph")
	for _, s := range seeds {
		if b, err := os.ReadFile(s); err == nil {
			f.Add(b)
		}
	}
	f.Add([]byte("CGPH\x01\x01\x00\x00"))
	f.Fuzz(func(t *testing.T, data []byte) {
		dir := t.TempDir()
		info := filepath.Join(dir, "info")
		if err := os.MkdirAll(info, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(info, "commit-graph"), data, 0o644); err != nil {
			t.Fatal(err)
		}
		g, err := Open(dir)
		if err != nil {
			return
		}
		defer g.Close()
		keys := g.Bloom().BloomKeys("a/b")
		var buf []uint32
		for pos := uint32(0); pos < uint32(g.Len()) && pos < 4096; pos++ {
			g.OID(pos)
			g.Tree(pos)
			g.CommitTime(pos)
			g.Generation(pos)
			g.NumParents(pos)
			buf, _ = g.Parents(pos, buf[:0])
			g.MaybeChanged(pos, keys)
			g.Lookup(g.OID(pos))
		}
	})
}
