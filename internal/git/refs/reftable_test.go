package refs

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
)

func reftableOf(r *gittest.Repo, hashSize int) *Reftable {
	common := strings.TrimSpace(r.Git("rev-parse", "--path-format=absolute", "--git-common-dir"))
	return NewReftable(r.GitDir(), common, hashSize)
}

func compareReader(t *testing.T, r *gittest.Repo, rd Reader) {
	t.Helper()
	l, err := rd.List()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for i := 0; i < l.Len(); i++ {
		got = append(got, strings.TrimRight(fmt.Sprintf("%s %x %s", l.Name(i), l.OID(i), l.Symbolic(i)), " "))
	}
	want := strings.TrimSpace(r.Git("for-each-ref", "--format=%(refname) %(objectname) %(symref)"))
	var wantLines []string
	for _, line := range strings.Split(want, "\n") {
		if line != "" {
			wantLines = append(wantLines, strings.TrimRight(line, " "))
		}
	}
	if strings.Join(got, "\n") != strings.Join(wantLines, "\n") {
		t.Fatalf("refs\n got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantLines, "\n"))
	}
	h, err := rd.Head()
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.TrimSpace(r.Git("rev-parse", "HEAD")); hex.EncodeToString(h.OID) != want {
		t.Fatalf("HEAD %x, want %s", h.OID, want)
	}
}

func TestReftable(t *testing.T) {
	for _, format := range []struct {
		args []string
		hash int
	}{{nil, 20}, {[]string{"--object-format=sha256"}, 32}} {
		r := gittest.Init(t, append([]string{"--ref-format=reftable"}, format.args...)...)
		rd := reftableOf(r, format.hash)
		if h, err := rd.Head(); err != nil || !h.Unborn || h.Target != "refs/heads/main" {
			t.Fatalf("unborn: %+v %v", h, err)
		}
		c1 := r.Commit("one")
		var stdin strings.Builder
		for i := 0; i < 3000; i++ {
			fmt.Fprintf(&stdin, "create refs/heads/b/%04d %s\n", i, c1)
		}
		r.Run([]byte(stdin.String()), "update-ref", "--stdin")
		r.Git("tag", "-a", "-m", "annotated", "v1")
		r.Git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/heads/main")
		r.Commit("two")
		for i := 0; i < 3000; i += 7 {
			r.Git("update-ref", "-d", fmt.Sprintf("refs/heads/b/%04d", i)) // tombstones
		}
		compareReader(t, r, rd)
		r.Git("pack-refs", "--all")
		compareReader(t, r, rd)
		l, _ := rd.List()
		peel := strings.TrimSpace(r.Git("rev-parse", "v1^{commit}"))
		for i := 0; i < l.Len(); i++ {
			if l.Name(i) == "refs/tags/v1" && hex.EncodeToString(l.Peeled(i)) != peel {
				t.Errorf("v1 peeled %x", l.Peeled(i))
			}
		}
		r.Git("checkout", "-q", "--detach")
		if h, _ := rd.Head(); !h.Detached {
			t.Error("detached HEAD")
		}
		if oid, _ := rd.Resolve("refs/remotes/origin/HEAD"); hex.EncodeToString(oid) != strings.TrimSpace(r.Git("rev-parse", "main")) {
			t.Errorf("resolve symref %x", oid)
		}
	}
}

// Small blocks make many blocks per table, with padding between them.
func TestReftableSmallBlocks(t *testing.T) {
	r := gittest.Init(t, "--ref-format=reftable")
	r.Git("config", "reftable.blockSize", "256")
	c := r.Commit("one")
	var stdin strings.Builder
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&stdin, "create refs/tags/t%03d-with-a-longer-name %s\n", i, c)
	}
	r.Run([]byte(stdin.String()), "update-ref", "--stdin")
	r.Git("pack-refs", "--all")
	compareReader(t, r, reftableOf(r, 20))
}

func TestReftableWorktree(t *testing.T) {
	r := gittest.Init(t, "--ref-format=reftable")
	r.Commit("one")
	wt := filepath.Join(t.TempDir(), "wt")
	r.Git("worktree", "add", "-q", "-b", "side", wt)
	w := gittest.Open(t, wt)
	w.Commit("on side")
	w.Git("bisect", "start")
	w.Git("bisect", "bad")
	rd := reftableOf(w, 20)
	h, err := rd.Head()
	if err != nil || h.Target != "refs/heads/side" {
		t.Fatalf("worktree HEAD %+v %v", h, err)
	}
	compareReader(t, w, rd)
	compareReader(t, r, reftableOf(r, 20))
}

func TestReftableCorrupt(t *testing.T) {
	r := gittest.Init(t, "--ref-format=reftable")
	c := r.Commit("one")
	var stdin strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&stdin, "create refs/heads/x%03d %s\n", i, c)
	}
	r.Run([]byte(stdin.String()), "update-ref", "--stdin")
	r.Git("pack-refs", "--all")
	dir := filepath.Join(r.GitDir(), "reftable")
	list, _ := os.ReadFile(filepath.Join(dir, "tables.list"))
	name := strings.Fields(string(list))[0]
	orig, _ := os.ReadFile(filepath.Join(dir, name))
	// Flip every byte of the body in turn, keeping the footer's checksum
	// valid, as the format has no checksum over blocks: no panic, and no
	// hang.
	for i := 0; i < len(orig)-72; i++ {
		data := append([]byte(nil), orig...)
		data[i] ^= 0x5a
		if i < 28 {
			copy(data[len(data)-72:], data[:28])
			binary.BigEndian.PutUint32(data[len(data)-4:], crc32.ChecksumIEEE(data[len(data)-72:len(data)-4]))
		}
		tab, err := openTable(data, 20)
		if err != nil {
			continue
		}
		merge([]*table{tab}, func([]byte, record) {})
	}
}

func FuzzReftable(f *testing.F) {
	r := gittest.Init(f, "--ref-format=reftable")
	r.Commit("one")
	r.Git("tag", "-a", "-m", "x", "v1")
	r.Git("pack-refs", "--all")
	dir := filepath.Join(r.GitDir(), "reftable")
	list, _ := os.ReadFile(filepath.Join(dir, "tables.list"))
	for _, name := range strings.Fields(string(list)) {
		data, _ := os.ReadFile(filepath.Join(dir, name))
		f.Add(data[:len(data)-68])
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		// The fuzzer mutates the body; the footer is rebuilt so the checks
		// past it run.
		if len(body) < 24 {
			return
		}
		data := append(append([]byte(nil), body...), make([]byte, 68)...)
		copy(data[len(body):], body[:24])
		binary.BigEndian.PutUint32(data[len(data)-4:], crc32.ChecksumIEEE(data[len(body):len(data)-4]))
		tab, err := openTable(data, 20)
		if err != nil {
			return
		}
		merge([]*table{tab}, func(name []byte, _ record) {
			if len(name) > maxName {
				t.Fatal("name over the cap")
			}
		})
	})
}
