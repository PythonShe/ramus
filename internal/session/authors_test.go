package session

import (
	"slices"
	"strconv"
	"testing"

	"ramus/internal/git/gittest"
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
