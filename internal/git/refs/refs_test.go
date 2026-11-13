package refs

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/gittest"
)

func store(r *gittest.Repo) *Store {
	gd := r.GitDir()
	common := strings.TrimSpace(r.Git("rev-parse", "--path-format=absolute", "--git-common-dir"))
	return New(gd, common, 20)
}

// compare lists refs with git for-each-ref.
func compare(t *testing.T, r *gittest.Repo) *List {
	t.Helper()
	l, err := store(r).List()
	if err != nil {
		t.Fatal(err)
	}
	want := strings.TrimSpace(r.Git("for-each-ref", "--format=%(refname) %(objectname) %(symref)"))
	var got []string
	for i := 0; i < l.Len(); i++ {
		got = append(got, strings.TrimRight(fmt.Sprintf("%s %x %s", l.Name(i), l.OID(i), l.Symbolic(i)), " "))
	}
	var wantLines []string
	for _, line := range strings.Split(want, "\n") {
		if line != "" {
			wantLines = append(wantLines, strings.TrimRight(line, " "))
		}
	}
	if strings.Join(got, "\n") != strings.Join(wantLines, "\n") {
		t.Fatalf("refs\n got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantLines, "\n"))
	}
	return l
}

func TestList(t *testing.T) {
	r := gittest.Init(t)
	c1 := r.Commit("one")
	r.Git("branch", "feature/a")
	r.Git("branch", "feature/b")
	r.Git("tag", "light")
	r.Git("tag", "-a", "-m", "annotated", "v1")
	r.Git("update-ref", "refs/remotes/origin/main", c1)
	r.Git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	r.Git("pack-refs", "--all")
	c2 := r.Commit("two") // a loose main overriding the packed one
	r.Git("tag", "-a", "-m", "loose annotated", "v2")
	r.Git("update-ref", "refs/heads/feature/b", c2)
	// A broken loose ref is ignored, as git ignores it.
	os.WriteFile(filepath.Join(r.GitDir(), "refs", "heads", "broken"), []byte("garbage\n"), 0o644)
	os.WriteFile(filepath.Join(r.GitDir(), "refs", "heads", "x.lock"), []byte(c1+"\n"), 0o644)
	r.Run(nil, "for-each-ref") // git prints a warning and goes on
	l := compare(t, r)
	if len(l.Broken) != 1 || l.Broken[0] != "refs/heads/broken" {
		t.Errorf("broken %q", l.Broken)
	}
	// packed-refs peels v1.
	peel := strings.TrimSpace(r.Git("rev-parse", "v1^{commit}"))
	for i := 0; i < l.Len(); i++ {
		if l.Name(i) == "refs/tags/v1" && hex.EncodeToString(l.Peeled(i)) != peel {
			t.Errorf("v1 peeled %x, want %s", l.Peeled(i), peel)
		}
	}
}

func TestHead(t *testing.T) {
	r := gittest.Init(t)
	s := store(r)
	h, err := s.Head()
	if err != nil || !h.Unborn || h.Target != "refs/heads/main" {
		t.Fatalf("unborn: %+v %v", h, err)
	}
	c := r.Commit("one")
	h, _ = s.Head()
	if h.Unborn || h.Detached || hex.EncodeToString(h.OID) != c {
		t.Fatalf("on main: %+v", h)
	}
	r.Git("checkout", "-q", "--detach")
	h, _ = s.Head()
	if !h.Detached || hex.EncodeToString(h.OID) != c {
		t.Fatalf("detached: %+v", h)
	}
	r.Git("checkout", "-q", "main")
	r.Git("pack-refs", "--all")
	h, _ = s.Head()
	if hex.EncodeToString(h.OID) != c {
		t.Fatalf("packed main: %+v", h)
	}
	head := filepath.Join(r.GitDir(), "HEAD")
	os.Remove(head)
	if os.Symlink("refs/heads/main", head) == nil {
		h, err = s.Head()
		if err != nil || h.Target != "refs/heads/main" || hex.EncodeToString(h.OID) != c {
			t.Fatalf("symlink HEAD: %+v %v", h, err)
		}
	}
}

func TestLinkedWorktree(t *testing.T) {
	r := gittest.Init(t)
	r.Commit("one")
	wt := filepath.Join(t.TempDir(), "wt")
	r.Git("worktree", "add", "-q", "-b", "side", wt)
	w := gittest.Open(t, wt)
	w.Git("bisect", "start")
	w.Git("bisect", "bad")
	h, err := store(w).Head()
	if err != nil || h.Target != "refs/heads/side" {
		t.Fatalf("worktree HEAD %+v %v", h, err)
	}
	compare(t, w)
	compare(t, r)
}

func TestValidName(t *testing.T) {
	good := []string{"refs/heads/main", "refs/heads/feature/x-1", "refs/tags/v1.0", "refs/heads/日本"}
	bad := []string{"refs/heads/a..b", "refs/heads/.hidden", "refs/heads/x.lock", "refs/heads/a b", "refs/heads/a:b",
		"refs/heads/a~1", "refs/heads/end.", "refs/heads//x", "refs/heads/@{u}", "refs/heads/a\x01"}
	for _, n := range good {
		if !ValidName(n) {
			t.Errorf("%q rejected", n)
		}
	}
	for _, n := range bad {
		if ValidName(n) {
			t.Errorf("%q accepted", n)
		}
	}
}

// TestKernel lists the kernel's refs when RAMUS_FIXTURES is set.
func TestKernel(t *testing.T) {
	dir := filepath.Join(gittest.FixturesDir(t), "linux")
	if _, err := os.Stat(dir); err != nil {
		t.Skip(err)
	}
	compare(t, gittest.Open(t, dir))
}

func FuzzPacked(f *testing.F) {
	f.Add([]byte("# pack-refs with: peeled fully-peeled sorted \n" +
		"1111111111111111111111111111111111111111 refs/heads/main\n" +
		"2222222222222222222222222222222222222222 refs/tags/v1\n" +
		"^3333333333333333333333333333333333333333\n"))
	f.Add([]byte("^3333333333333333333333333333333333333333\nbad line\n"))
	s := New("", "", 20)
	f.Fuzz(func(t *testing.T, data []byte) {
		for name, r := range s.parsePacked(data) {
			if !ValidName(name) || len(r.oid) != 20 || (r.peeled != nil && len(r.peeled) != 20) {
				t.Fatalf("accepted %q %+v", name, r)
			}
		}
	})
}

// A FIFO, a huge file or a symlink leaving refs/ is not read as a ref.
func TestHostileLooseRefs(t *testing.T) {
	r := gittest.Init(t)
	c := r.Commit("one")
	heads := filepath.Join(r.GitDir(), "refs", "heads")
	if err := syscallMkfifo(filepath.Join(heads, "fifo")); err != nil {
		t.Log("no FIFO:", err)
	}
	big, _ := os.Create(filepath.Join(heads, "big"))
	big.Truncate(256 << 20) // sparse
	big.Close()
	os.Symlink("refs/../../../outside", filepath.Join(heads, "escape"))
	os.WriteFile(filepath.Join(r.Dir, "outside"), []byte(c+"\n"), 0o644)
	done := make(chan *List)
	go func() {
		l, err := store(r).List()
		if err != nil {
			t.Error(err)
		}
		done <- l
	}()
	select {
	case l := <-done:
		if l == nil {
			return
		}
		for i := 0; i < l.Len(); i++ {
			if n := l.Name(i); n != "refs/heads/main" {
				t.Errorf("read %s", n)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("listing refs blocked")
	}
}
