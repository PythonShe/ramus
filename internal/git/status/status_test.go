package status

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

// format prints a status back as porcelain v2 -z, entries only.
func format(s *Status) string {
	var b strings.Builder
	for i := 0; i < s.Len(); i++ {
		x, y := s.XY(i)
		m := s.Modes(i)
		ids := s.OIDs(i)
		switch s.Kind(i) {
		case Untracked, Ignored:
			fmt.Fprintf(&b, "%c %s\x00", s.Kind(i), s.Path(i))
		case Changed:
			fmt.Fprintf(&b, "1 %c%c %s %06o %06o %06o %x %x %s\x00", x, y, s.Submodule(i), m[0], m[1], m[2], ids[0], ids[1], s.Path(i))
		case Renamed:
			sc, cp := s.Score(i)
			letter := 'R'
			if cp {
				letter = 'C'
			}
			fmt.Fprintf(&b, "2 %c%c %s %06o %06o %06o %x %x %c%d %s\x00%s\x00", x, y, s.Submodule(i), m[0], m[1], m[2], ids[0], ids[1], letter, sc, s.Path(i), s.OrigPath(i))
		case Unmerged:
			fmt.Fprintf(&b, "u %c%c %s %06o %06o %06o %06o %x %x %x %s\x00", x, y, s.Submodule(i), m[0], m[1], m[2], m[3], ids[0], ids[1], ids[2], s.Path(i))
		}
	}
	return b.String()
}

func entriesOnly(out string) string {
	var b strings.Builder
	parts := strings.Split(out, "\x00")
	for i := 0; i < len(parts); i++ {
		p := parts[i]
		if p == "" || strings.HasPrefix(p, "# ") {
			continue
		}
		b.WriteString(p + "\x00")
		if strings.HasPrefix(p, "2 ") {
			i++
			b.WriteString(parts[i] + "\x00")
		}
	}
	return b.String()
}

func TestStatus(t *testing.T) {
	up := gittest.Init(t)
	up.Commit("base")
	r := gittest.Open(t, filepath.Join(t.TempDir(), "clone"))
	up.Git("clone", "-q", up.Dir, r.Dir)
	r.Write("conflict", "base\n")
	r.Write("gone", "x\n")
	r.Write("moved", strings.Repeat("long enough to be a rename\n", 5))
	r.Write("dir/keep", "k\n")
	r.Write(".gitignore", "*.log\n")
	r.Commit("files")
	r.Git("checkout", "-q", "-b", "other")
	r.Write("conflict", "theirs\n")
	r.Commit("theirs")
	r.Git("checkout", "-q", "main")
	r.Write("conflict", "ours\n")
	r.Commit("ours")
	r.Write("stashed", "s\n")
	r.Git("add", "stashed")
	r.Git("stash", "-q")
	r.Run(nil, "merge", "-q", "other") // conflicts, on purpose
	r.Git("mv", "moved", "dir/moved")
	r.Write("dir/keep", "k2\n")
	os.Remove(filepath.Join(r.Dir, "gone"))
	r.Write("new.txt", "n\n")
	r.Write("untracked dir/a", "a\n")
	r.Write("x.log", "ignored\n")
	r.Write("日本 語", "u\n")
	os.Chmod(filepath.Join(r.Dir, ".gitignore"), 0o755)

	run := testrun.Runner(t)
	for _, opt := range []Options{{}, {Ignored: true, UntrackedAll: true}} {
		s, err := Read(context.Background(), run, r.GitDir(), r.Dir, 20, opt)
		if err != nil {
			t.Fatal(err)
		}
		args := []string{"status", "--porcelain=v2", "-z", "--branch", "--show-stash", "--no-ahead-behind", "--ignore-submodules=dirty"}
		if opt.Ignored {
			args = append(args, "--ignored=matching", "--untracked-files=all")
		}
		want := entriesOnly(r.Git(args...))
		if got := format(s); got != want {
			t.Fatalf("%+v\n got %q\nwant %q", opt, got, want)
		}
		b := s.Branch
		if b.Head != "main" || b.Upstream != "origin/main" || b.Stashes != 1 || len(b.OID) != 40 || b.Detached {
			t.Errorf("branch %+v", b)
		}
		kinds := map[Kind]int{}
		for i := 0; i < s.Len(); i++ {
			kinds[s.Kind(i)]++
		}
		if kinds[Unmerged] != 1 || s.Unmerged() != 1 || kinds[Renamed] != 1 || kinds[Untracked] < 3 || (opt.Ignored && kinds[Ignored] != 1) {
			t.Errorf("%+v: kinds %v", opt, kinds)
		}
	}
}

func TestInitialAndDetached(t *testing.T) {
	r := gittest.Init(t)
	run := testrun.Runner(t)
	s, err := Read(context.Background(), run, r.GitDir(), r.Dir, 20, Options{})
	if err != nil || s.Branch.OID != "" || s.Branch.Head != "main" {
		t.Fatalf("initial %+v %v", s, err)
	}
	r.Commit("one")
	r.Git("checkout", "-q", "--detach")
	s, _ = Read(context.Background(), run, r.GitDir(), r.Dir, 20, Options{})
	if !s.Branch.Detached || s.Branch.Head != "" {
		t.Errorf("detached %+v", s.Branch)
	}
}

// TestMonorepo reads the status of the 390,000-file fixture, clean, when
// RAMUS_FIXTURES is set. Working numbers: the machine is shared.
func TestMonorepo(t *testing.T) {
	dir := filepath.Join(gittest.FixturesDir(t), "monorepo.noindex")
	if _, err := os.Stat(dir); err != nil {
		t.Skip(err)
	}
	t0 := time.Now()
	s, err := Read(context.Background(), testrun.Runner(t), filepath.Join(dir, ".git"), dir, 20, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d entries in %v", s.Len(), time.Since(t0))
}

func FuzzParse(f *testing.F) {
	f.Add([]byte("# branch.oid (initial)\x00# branch.head main\x001 .M N... 100644 100644 100644 " + strings.Repeat("a", 40) + " " + strings.Repeat("b", 40) + " p\x002 R. N... 100644 100644 100644 " + strings.Repeat("a", 40) + " " + strings.Repeat("b", 40) + " R90 new\x00old\x00? u\x00"))
	f.Fuzz(func(t *testing.T, data []byte) {
		s, err := Parse(data, 20)
		if err != nil {
			return
		}
		for i := 0; i < s.Len(); i++ {
			_, _ = s.Path(i), s.OrigPath(i)
			for _, o := range s.OIDs(i) {
				if len(o) != 20 {
					t.Fatal("oid length")
				}
			}
		}
	})
}
