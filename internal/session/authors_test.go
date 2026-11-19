package session

import (
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/gitexe"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/repo"
	"ramus/internal/git/run"
)

// The people are the authors git log names through the mailmap, one per
// address, those with the most commits first, without the user; read
// alike with and without a commit-graph.
func TestLoadAuthors(t *testing.T) {
	r := gittest.Init(t)
	r.Git("config", "user.email", "TEST@example.com")
	commit := func(name, email string) {
		r.Env = []string{"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email}
		r.Write("f", name+email+strconv.Itoa(len(r.Git("rev-list", "--all"))))
		r.Commit("c")
		r.Env = nil
	}
	r.Write(".mailmap", "Ada Lovelace <ada@example.com>\nAda Lovelace <ada@example.com> <ada@old.example.com>\n")
	commit("Bob", "bob@example.com")
	commit("Ada", "ada@example.com")
	commit("ada l", "ada@old.example.com")
	commit("Me", "test@example.com")
	commit("Ada", "ada@example.com")
	want := []Person{{"Ada Lovelace", "ada@example.com"}, {"Bob", "bob@example.com"}}
	load := func() []Person {
		s, m := openFor(t, r)
		var got []Person
		done := false
		s.LoadAuthors(func(p []Person) { got, done = p, true })
		m.until(t, "the authors", func() bool { return done })
		return got
	}
	if got := load(); !slices.Equal(got, want) {
		t.Fatalf("without a commit-graph got %q, want %q", got, want)
	}
	r.Git("commit-graph", "write", "--reachable")
	if got := load(); !slices.Equal(got, want) {
		t.Fatalf("with a commit-graph got %q, want %q", got, want)
	}
}

// The commits graphRecent lists stay readable after the commit-graph
// they came from is replaced and unmapped.
func TestGraphRecentOutlivesTheGraph(t *testing.T) {
	g := gittest.Init(t)
	for range 5 {
		g.Write("f", strconv.Itoa(len(g.Git("rev-list", "--all"))))
		g.Commit("c")
	}
	g.Git("commit-graph", "write", "--reachable")
	r, err := repo.Open(t.Context(), testrun.Runner(t), g.Dir, func(repo.Location) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	tip, _ := hex.DecodeString(strings.TrimSpace(g.Git("rev-parse", "HEAD")))
	ids := graphRecent(r, tip, 5)
	want := make([]string, len(ids))
	for i, id := range ids {
		want[i] = hex.EncodeToString(id)
	}
	for range 5 {
		g.Commit("more")
		g.Git("commit-graph", "write", "--reachable")
		if _, err := r.CommitGraph(); err != nil {
			t.Fatal(err)
		}
		runtime.GC()
		time.Sleep(10 * time.Millisecond) // cleanups run on a goroutine of their own
	}
	for i, id := range ids {
		if got := hex.EncodeToString(id); got != want[i] {
			t.Fatalf("commit %d reads %s after the graph went, was %s", i, got, want[i])
		}
	}
}

// A read that failed is not kept as the answer for its HEAD: the next
// call reads again.
func TestFailedAuthorsReadAgain(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as git")
	}
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip(err)
	}
	r := gittest.Init(t)
	r.Env = []string{"GIT_AUTHOR_NAME=Bob", "GIT_AUTHOR_EMAIL=bob@example.com"}
	r.Write("f", "1")
	r.Commit("c")
	r.Env = nil
	bin := t.TempDir()
	fail := filepath.Join(bin, "fail")
	// While fail exists, cat-file fails.
	script := "#!/bin/sh\nfor a in \"$@\"; do [ \"$a\" = cat-file ] && [ -e " + fail + " ] && exit 1; done\nexec " + real + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fail, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := gitexe.Find(t.Context(), bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err != nil {
		t.Fatal(err)
	}
	s, m := openWith(t, r, run.New(g, gittest.Env(), 8))
	load := func() []Person {
		var got []Person
		done := false
		s.LoadAuthors(func(p []Person) { got, done = p, true })
		m.until(t, "the authors", func() bool { return done })
		return got
	}
	if got := load(); len(got) != 0 {
		t.Fatalf("read with cat-file failing: %q", got)
	}
	os.Remove(fail)
	if got := load(); !slices.Equal(got, []Person{{"Bob", "bob@example.com"}}) {
		t.Fatalf("read again got %q", got)
	}
}
