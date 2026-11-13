package commitgraph

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
)

// fixture builds a history with the shapes the reader must handle: roots,
// merges, an octopus merge, a commit older than its parent, one dated past
// 2106 (34-bit time), one far older than its parent (generation overflow),
// paths with bytes above 0x7f, an empty commit, and a commit touching more
// files than a filter holds.
func fixture(t *testing.T) *gittest.Repo {
	t.Helper()
	r := gittest.Init(t)
	r.Write("README", "one\n")
	r.Commit("root")
	r.Write("src/a.go", "package a\n")
	r.Write("src/deep/dir/b.go", "package b\n")
	r.Commit("add src")
	r.Git("checkout", "-q", "-b", "topic")
	r.Write("docs/café/日本.md", "unicode\n")
	r.Commit("unicode paths")
	r.Git("checkout", "-q", "main")
	r.Write("src/a.go", "package a // changed\n")
	r.Commit("change a")
	for i := 0; i < 3; i++ {
		r.Git("checkout", "-q", "-b", fmt.Sprintf("o%d", i), "main")
		r.Write(fmt.Sprintf("oct/%d.txt", i), "x\n")
		r.Commit(fmt.Sprintf("octo %d", i))
	}
	r.Git("checkout", "-q", "main")
	r.Env = append(r.Env, fmt.Sprintf("GIT_COMMITTER_DATE=@%d +0000", r.Tick()))
	r.Git("merge", "-q", "--no-ff", "-m", "merge topic", "topic")
	r.Git("merge", "-q", "--no-ff", "-m", "octopus", "o0", "o1", "o2")
	r.Env = nil
	r.CommitAt("clock went backwards", 1_600_000_000)
	r.CommitAt("far future", 1<<32+5)
	r.CommitAt("long ago", 100)
	r.Commit("empty")
	for i := 0; i < 600; i++ {
		r.Write(fmt.Sprintf("many/f%03d", i), "x\n")
	}
	r.Commit("many files")
	// A path that is not UTF-8, which APFS will not create, on a branch that
	// nothing checks out again.
	r.Git("checkout", "-q", "-b", "bytes")
	r.Stage("docs/\xff\xfe.bin", "high bytes\n")
	r.CommitIndex("high-byte path")
	r.Git("checkout", "-q", "-f", "main")
	// A second root, on its own branch.
	r.Git("checkout", "-q", "--orphan", "other")
	r.Git("rm", "-q", "-rf", ".")
	r.Write("other.txt", "other\n")
	r.Commit("second root")
	r.Git("checkout", "-q", "main")
	return r
}

type oracleCommit struct {
	oid     string
	tree    string
	time    int64
	parents []string
}

func oracle(t *testing.T, r *gittest.Repo) []oracleCommit {
	out := r.Git("log", "--all", "--format=%H %T %ct %P")
	var cs []oracleCommit
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Fields(line)
		ct, err := strconv.ParseInt(f[2], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		cs = append(cs, oracleCommit{oid: f[0], tree: f[1], time: ct, parents: f[3:]})
	}
	return cs
}

func open(t *testing.T, r *gittest.Repo) *Graph {
	t.Helper()
	g, err := Open(filepath.Join(r.GitDir(), "objects"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	return g
}

func hexOf(b []byte) string { return hex.EncodeToString(b) }

func TestNotFound(t *testing.T) {
	r := gittest.Init(t)
	r.Commit("x")
	if _, err := Open(filepath.Join(r.GitDir(), "objects")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestOracle(t *testing.T) {
	variants := []struct {
		name   string
		config []string
		split  bool
		layers int
		genV2  bool
		bloom  uint32
	}{
		{name: "single v2", config: []string{"commitGraph.changedPathsVersion=2"}, layers: 1, genV2: true, bloom: 2},
		{name: "single v1 bloom", config: []string{"commitGraph.changedPathsVersion=1"}, layers: 1, genV2: true, bloom: 1},
		{name: "topo levels only", config: []string{"commitGraph.generationVersion=1", "commitGraph.changedPathsVersion=2"}, layers: 1, genV2: false, bloom: 2},
		{name: "split chain", config: []string{"commitGraph.changedPathsVersion=2"}, split: true, layers: 2, genV2: true, bloom: 2},
	}
	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			r := fixture(t)
			var cfg []string
			for _, c := range v.config {
				cfg = append(cfg, "-c", c)
			}
			write := func(extra ...string) {
				args := append(append([]string{}, cfg...), "commit-graph", "write", "--changed-paths")
				r.Git(append(args, extra...)...)
			}
			if v.split {
				// The first layer holds main's first commits; the second
				// adds the rest.
				sub := r.Git("rev-parse", "main~6")
				if _, err := r.Run([]byte(sub), append(append([]string{}, cfg...), "commit-graph", "write", "--changed-paths", "--split=no-merge", "--stdin-commits")...); err != nil {
					t.Fatal(err)
				}
				write("--reachable", "--split=no-merge")
			} else {
				write("--reachable")
			}
			g := open(t, r)
			if g.Layers() != v.layers {
				t.Fatalf("layers = %d, want %d", g.Layers(), v.layers)
			}
			if g.HasGenerationV2() != v.genV2 {
				t.Fatalf("generation v2 = %v, want %v", g.HasGenerationV2(), v.genV2)
			}
			if g.Bloom().Version != v.bloom {
				t.Fatalf("bloom version = %d, want %d", g.Bloom().Version, v.bloom)
			}
			checkCommits(t, g, oracle(t, r))
			checkBloom(t, r, g)
		})
	}
}

func checkCommits(t *testing.T, g *Graph, cs []oracleCommit) {
	t.Helper()
	if g.Len() != len(cs) {
		t.Fatalf("graph has %d commits, git %d", g.Len(), len(cs))
	}
	pos := map[string]uint32{}
	for _, c := range cs {
		oid, _ := hex.DecodeString(c.oid)
		p := g.Lookup(oid)
		if p == None {
			t.Fatalf("%s not found", c.oid)
		}
		if hexOf(g.OID(p)) != c.oid {
			t.Fatalf("OID(%d) = %s, want %s", p, hexOf(g.OID(p)), c.oid)
		}
		pos[c.oid] = p
	}
	byOID := map[string]oracleCommit{}
	for _, c := range cs {
		byOID[c.oid] = c
	}
	// Generation numbers as git defines them, computed from the oracle.
	level := map[string]uint64{}
	corrected := map[string]uint64{}
	var gen func(string)
	gen = func(oid string) {
		if _, ok := level[oid]; ok {
			return
		}
		c := byOID[oid]
		lv, cd := uint64(1), uint64(c.time)
		for _, p := range c.parents {
			gen(p)
			lv = max(lv, level[p]+1)
			cd = max(cd, corrected[p]+1)
		}
		level[oid], corrected[oid] = lv, cd
	}
	for _, c := range cs {
		p := pos[c.oid]
		if got := hexOf(g.Tree(p)); got != c.tree {
			t.Errorf("%s tree %s, want %s", c.oid, got, c.tree)
		}
		if got := g.CommitTime(p); got != c.time {
			t.Errorf("%s time %d, want %d", c.oid, got, c.time)
		}
		ps, err := g.Parents(p, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(ps) != len(c.parents) || g.NumParents(p) != len(c.parents) {
			t.Fatalf("%s has %d parents, want %d", c.oid, len(ps), len(c.parents))
		}
		for i, pp := range ps {
			if hexOf(g.OID(pp)) != c.parents[i] {
				t.Errorf("%s parent %d is %s, want %s", c.oid, i, hexOf(g.OID(pp)), c.parents[i])
			}
		}
		gen(c.oid)
		if got := uint64(g.TopoLevel(p)); got != level[c.oid] {
			t.Errorf("%s topological level %d, want %d", c.oid, got, level[c.oid])
		}
		want := level[c.oid]
		if g.HasGenerationV2() {
			want = corrected[c.oid]
		}
		if got := g.Generation(p); got != want {
			t.Errorf("%s generation %d, want %d", c.oid, got, want)
		}
	}
	if g.Lookup(make([]byte, g.HashSize())) != None {
		t.Error("found the zero id")
	}
	if g.Lookup([]byte{1, 2}) != None {
		t.Error("found a short id")
	}
}

// checkBloom rebuilds every commit's filter from git's own list of changed
// paths and compares it bit for bit with the stored one.
func checkBloom(t *testing.T, r *gittest.Repo, g *Graph) {
	t.Helper()
	s := g.Bloom()
	for pos := uint32(0); pos < uint32(g.Len()); pos++ {
		oid := hexOf(g.OID(pos))
		ps, _ := g.Parents(pos, nil)
		var out string
		if len(ps) == 0 {
			out = r.Git("diff-tree", "-r", "--root", "--no-commit-id", "--name-only", "-z", oid)
		} else {
			out = r.Git("diff-tree", "-r", "--no-commit-id", "--name-only", "-z", hexOf(g.OID(ps[0])), oid)
		}
		set := map[string]bool{}
		var changed []string
		for _, p := range strings.Split(out, "\x00") {
			if p == "" {
				continue
			}
			changed = append(changed, p)
			for q := p; q != ""; {
				set[q] = true
				i := strings.LastIndexByte(q, '/')
				if i < 0 {
					break
				}
				q = q[:i]
			}
		}
		var want []byte
		switch {
		case len(set) > int(s.MaxChangedPaths):
			want = []byte{0xff}
		case len(set) == 0:
			want = []byte{0}
		default:
			want = make([]byte, (len(set)*int(s.BitsPerEntry)+7)/8)
			paths := make([]string, 0, len(set))
			for p := range set {
				paths = append(paths, p)
			}
			sort.Strings(paths)
			for _, p := range paths {
				for _, h := range s.Key(p).hashes {
					bit := uint64(h) % (uint64(len(want)) * 8)
					want[bit/8] |= 1 << (bit % 8)
				}
			}
		}
		if got := g.filter(pos); !bytes.Equal(got, want) {
			t.Errorf("%s filter %x, want %x (%d paths)", oid, got, want, len(set))
			continue
		}
		for _, p := range changed {
			if res := g.MaybeChanged(pos, s.BloomKeys(p)); res == BloomNo {
				t.Errorf("%s: filter says %q unchanged", oid, p)
			}
		}
	}
}

func TestDamaged(t *testing.T) {
	r := fixture(t)
	r.Git("commit-graph", "write", "--reachable", "--changed-paths")
	path := filepath.Join(r.GitDir(), "objects", "info", "commit-graph")
	good := readFile(t, path)
	cases := map[string]func([]byte) []byte{
		"truncated":    func(b []byte) []byte { return b[:20] },
		"signature":    func(b []byte) []byte { b[0] = 'X'; return b },
		"version":      func(b []byte) []byte { b[4] = 9; return b },
		"hash":         func(b []byte) []byte { b[5] = 7; return b },
		"chunk count":  func(b []byte) []byte { b[6] = 200; return b },
		"chunk offset": func(b []byte) []byte { b[8+4] = 0xff; return b },
		"fanout order": func(b []byte) []byte { return corruptFanout(b) },
		"empty file":   func(b []byte) []byte { return nil },
	}
	for name, corrupt := range cases {
		t.Run(name, func(t *testing.T) {
			writeFile(t, path, corrupt(append([]byte(nil), good...)))
			g, err := Open(filepath.Join(r.GitDir(), "objects"))
			if err == nil {
				g.Close()
				t.Fatal("opened a damaged file")
			}
		})
	}
}

func corruptFanout(b []byte) []byte {
	n := int(b[6])
	for i := 0; i < n; i++ {
		e := b[8+i*12:]
		if string(e[:4]) == "OIDF" {
			off := int(uint64(e[4])<<56 | uint64(e[5])<<48 | uint64(e[6])<<40 | uint64(e[7])<<32 | uint64(e[8])<<24 | uint64(e[9])<<16 | uint64(e[10])<<8 | uint64(e[11]))
			b[off] = 0xff // the first bucket now exceeds the next
		}
	}
	return b
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writeFile(t *testing.T, path string, b []byte) {
	t.Helper()
	// git writes the graph read-only.
	os.Chmod(path, 0o644)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}
