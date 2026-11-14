package gittest

import (
	"encoding/binary"
	"os"
	"path/filepath"
)

// WriteGraphWithoutGenerations writes the repository's commit-graph as a
// git older than 2.19 did: no generation data chunk, and every
// topological level zero. Today's git writes no such file, so one it
// writes is rewritten in place.
func (r *Repo) WriteGraphWithoutGenerations() {
	r.t.Helper()
	r.Git("-c", "commitGraph.generationVersion=1", "commit-graph", "write", "--reachable")
	path := filepath.Join(r.GitDir(), "objects", "info", "commit-graph")
	b, err := os.ReadFile(path)
	if err != nil {
		r.t.Fatal(err)
	}
	const header, entry = 8, 12
	hashSize := 20
	if b[5] == 2 {
		hashSize = 32
	}
	var fanout, cdat []byte
	for i := 0; i < int(b[6]); i++ {
		e := b[header+i*entry:]
		off, next := binary.BigEndian.Uint64(e[4:]), binary.BigEndian.Uint64(e[16:])
		switch string(e[:4]) {
		case "OIDF":
			fanout = b[off:next]
		case "CDAT":
			cdat = b[off:next]
		}
	}
	if fanout == nil || cdat == nil {
		r.t.Fatal("commit-graph without fanout or commit data")
	}
	n := int(binary.BigEndian.Uint32(fanout[255*4:]))
	for i := 0; i < n; i++ {
		// The top 30 bits are the level, the low 2 the time's high bits.
		w := cdat[i*(hashSize+16)+hashSize+8:]
		binary.BigEndian.PutUint32(w, binary.BigEndian.Uint32(w)&3)
	}
	// git writes the graph read-only.
	os.Chmod(path, 0o644)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		r.t.Fatal(err)
	}
}
