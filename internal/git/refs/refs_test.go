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
	// packed-refs peels v1, and says the packed light tag is no annotated
	// tag; of the loose v2 nothing is known without reading it.
	peel := strings.TrimSpace(r.Git("rev-parse", "v1^{commit}"))
	for i := 0; i < l.Len(); i++ {
		switch l.Name(i) {
		case "refs/tags/v1":
			if hex.EncodeToString(l.Peeled(i)) != peel || !l.PeelKnown(i) {
				t.Errorf("v1 peeled %x, want %s", l.Peeled(i), peel)
			}
		case "refs/tags/light":
			if l.Peeled(i) != nil || !l.PeelKnown(i) {
				t.Errorf("light peeled %x, known %v", l.Peeled(i), l.PeelKnown(i))
			}
		case "refs/tags/v2", "refs/remotes/origin/HEAD":
			if l.PeelKnown(i) {
				t.Errorf("%s known peeled", l.Name(i))
			}
		}
	}
	for i := 0; i < l.Len(); i++ {
		if j, ok := l.Find(l.Name(i)); !ok || j != i {
			t.Errorf("Find(%q) = %d %v, want %d", l.Name(i), j, ok, i)
		}
	}
	for _, name := range []string{"", "refs/heads/feature", "refs/heads/feature/c", "refs/tags/v3", "zzz"} {
		if _, ok := l.Find(name); ok {
			t.Errorf("Find(%q) found a ref", name)
		}
	}
}

// Edges where git's reading of loose refs is looser or stricter than it
// looks: text after the object id, a broken loose ref over a packed one,
// and a symbolic ref to nothing.
func TestListEdges(t *testing.T) {
	r := gittest.Init(t)
	c := r.Commit("one")
	for _, b := range []string{"empty", "garbage", "kept"} {
		r.Git("branch", b)
	}
	r.Git("pack-refs", "--all")
	heads := filepath.Join(r.GitDir(), "refs", "heads")
	for name, data := range map[string]string{
		"trail":   c + " trailing text\n",
		"tab":     c + "\tafter a tab\n",
		"glued":   c + "x\n",
		"indent":  " " + c + "\n",
		"empty":   "",
		"garbage": "garbage\n",
		"null":    strings.Repeat("0", 40) + "\n",
	} {
		if err := os.WriteFile(filepath.Join(heads, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r.Git("symbolic-ref", "refs/heads/dangling", "refs/heads/nothere")
	r.Git("symbolic-ref", "refs/heads/good", "refs/heads/main")
	l := compare(t, r)
	if len(l.Broken) != 5 {
		t.Errorf("broken %q", l.Broken)
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

// A branch whose loose path is a folder, as refs/heads/feature is when
// refs/heads/feature/x exists, is looked for in packed-refs and is
// otherwise unborn, as git reads it.
func TestBranchNamedLikeAFolder(t *testing.T) {
	r := gittest.Init(t)
	c := r.Commit("one")
	r.Git("branch", "feature/x")
	r.Git("symbolic-ref", "HEAD", "refs/heads/feature")
	s := store(r)
	h, err := s.Head()
	if err != nil || !h.Unborn || h.Target != "refs/heads/feature" {
		t.Fatalf("unborn: %+v %v", h, err)
	}
	if got := r.Git("status", "--porcelain=v2", "--branch"); !strings.Contains(got, "# branch.oid (initial)") {
		t.Fatalf("git says %q", got)
	}
	// packed-refs may still hold a branch of that name from before.
	packed := filepath.Join(r.GitDir(), "packed-refs")
	if err := os.WriteFile(packed, []byte(c+" refs/heads/feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err = s.Head()
	if err != nil || h.Unborn || hex.EncodeToString(h.OID) != c {
		t.Fatalf("packed: %+v %v", h, err)
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
	case <-time.After(time.Minute):
		t.Fatal("listing refs blocked")
	}
}
