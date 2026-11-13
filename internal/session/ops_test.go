package session

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/write"
)

func openFor(t *testing.T, r *gittest.Repo) (*Session, mainThread) {
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "the status", func() bool { return s.State.Status != nil })
	return s, m
}

// The clean listing is git clean's, odd names unquoted, ignored files
// only when asked.
func TestListClean(t *testing.T) {
	r := gittest.Init(t)
	r.Write(".gitignore", "*.log\n")
	r.Commit("ignore")
	r.Write("dir/x", "1")
	r.Write("a\"b\tc", "1")
	r.Write("y.log", "1")
	s, m := openFor(t, r)
	list := func(ignored bool) []string {
		var got []string
		done := false
		s.ListClean(ignored, func(p []string, _ []write.FileState, err error) {
			if err != nil {
				t.Error(err)
			}
			got, done = p, true
		})
		m.until(t, "the list", func() bool { return done })
		slices.Sort(got)
		return got
	}
	if got := list(false); !slices.Equal(got, []string{"a\"b\tc", "dir/"}) {
		t.Fatalf("got %q", got)
	}
	if got := list(true); !slices.Equal(got, []string{"a\"b\tc", "dir/", "y.log"}) {
		t.Fatalf("with ignored got %q", got)
	}
}

func TestLoadBisect(t *testing.T) {
	r := gittest.Init(t)
	var commits []string
	for i := range 16 {
		r.Write("n", strings.Repeat("x", i+1))
		commits = append(commits, r.Commit("step"))
	}
	s, m := openFor(t, r)
	load := func() BisectState {
		var got *BisectState
		s.LoadBisect(func(b BisectState, err error) {
			if err != nil {
				t.Error(err)
			}
			got = &b
		})
		m.until(t, "the bisect", func() bool { return got != nil })
		return *got
	}
	if load().Active {
		t.Fatal("active before starting")
	}
	r.Git("bisect", "start", commits[15], commits[0])
	b := load()
	if !b.Active || b.Bad != commits[15] || len(b.Good) != 1 || b.Left < 1 || b.Steps < 1 || b.FirstBad != "" {
		t.Fatalf("started: %+v", b)
	}
	// Mark until git names the first bad commit; it is commit 9.
	for range 10 {
		head := strings.TrimSpace(r.Git("rev-parse", "HEAD"))
		verdict := "good"
		if slices.Index(commits, head) >= 9 {
			verdict = "bad"
		}
		out := r.Git("bisect", verdict)
		if strings.Contains(out, "is the first bad commit") {
			break
		}
	}
	if b := load(); b.FirstBad != commits[9] || b.Left != 0 {
		t.Fatalf("found: %+v, want %s", b, commits[9])
	}
}

func TestSaveAndReadPatch(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a", "1\n")
	r.Commit("one")
	r.Write("a", "2\n")
	c1 := r.Commit("two")
	r.Git("mv", "a", "b")
	c2 := r.Commit("move")
	r.Write("b", "3\n")
	s, m := openFor(t, r)
	dir := t.TempDir()
	save := func(req PatchSave, name string) string {
		file := filepath.Join(dir, name)
		done := false
		s.SavePatch(req, file, func(err error) {
			if err != nil {
				t.Error(err)
			}
			done = true
		})
		m.until(t, "the save", func() bool { return done })
		return file
	}
	read := func(file string) PatchFile {
		var got *PatchFile
		s.ReadPatchFile(file, func(pf PatchFile, err error) {
			if err != nil {
				t.Error(err)
			}
			got = &pf
		})
		m.until(t, "the read", func() bool { return got != nil })
		return *got
	}
	mbox := save(PatchSave{Commits: []string{c1, c2}}, "two.patch")
	if b, _ := os.ReadFile(mbox); strings.Count(string(b), "\nSubject: [PATCH") != 2 || !strings.HasPrefix(string(b), "From "+c1) {
		t.Fatalf("mailbox:\n%s", b)
	}
	if pf := read(mbox); !pf.Mailbox || !slices.Equal(pf.Files, []string{"a", "b"}) {
		t.Fatalf("mailbox read %+v", pf)
	}
	plain := save(PatchSave{Paths: []string{"b"}}, "b.diff")
	if pf := read(plain); pf.Mailbox || !slices.Equal(pf.Files, []string{"b"}) {
		t.Fatalf("plain read %+v", pf)
	}
	os.WriteFile(filepath.Join(dir, "junk"), []byte("not a patch\n"), 0o644)
	done := false
	s.ReadPatchFile(filepath.Join(dir, "junk"), func(pf PatchFile, err error) {
		if err == nil && len(pf.Files) > 0 {
			t.Errorf("junk read as %+v", pf)
		}
		done = true
	})
	m.until(t, "the junk", func() bool { return done })
}
