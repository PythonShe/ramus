package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/run"
)

// git difftool runs the tool the config names on the paths asked for,
// and a repository not trusted runs none.
func TestDiffTool(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a.txt", "one\n")
	r.Commit("a")
	r.Write("a.txt", "two\n")
	out := filepath.Join(t.TempDir(), "seen")
	r.Git("config", "difftool.record.cmd", `cat "$LOCAL" "$REMOTE" > `+filepath.ToSlash(out))
	r.Git("config", "diff.tool", "record")

	m := make(mainThread, 64)
	open := func(trust bool) *Session {
		ts := trusted(t)
		if trust {
			ts = trusted(t, r.GitDir())
		}
		s := Open(testrun.Runner(t), ts, r.Dir, m.update)
		t.Cleanup(func() { s.Close(); <-s.Done() })
		m.until(t, "the status", func() bool { return s.State.Status != nil })
		return s
	}
	run1 := func(s *Session, req DiffToolRequest) error {
		var got error
		done := false
		s.OpenDiffTool(req, func(err error) { got, done = err, true })
		m.until(t, "the tool", func() bool { return done })
		return got
	}
	if err := run1(open(false), DiffToolRequest{Paths: []string{"a.txt"}}); !errors.Is(err, run.ErrRestricted) {
		t.Fatalf("untrusted: %v", err)
	}
	s := open(true)
	if err := run1(s, DiffToolRequest{Paths: []string{"a.txt"}}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(out); string(b) != "one\ntwo\n" {
		t.Fatalf("the tool saw %q", b)
	}
	head := mustHex(t, strings.TrimSpace(r.Git("rev-parse", "HEAD")))
	r.Commit("b")
	next := mustHex(t, strings.TrimSpace(r.Git("rev-parse", "HEAD")))
	if err := run1(s, DiffToolRequest{Paths: []string{"a.txt"}, From: head, To: next}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(out); string(b) != "one\ntwo\n" {
		t.Fatalf("between commits the tool saw %q", b)
	}
	r.Git("config", "--unset", "diff.tool")
	if err := run1(open(true), DiffToolRequest{Paths: []string{"a.txt"}}); !errors.Is(err, ErrNoDiffTool) {
		t.Fatalf("no tool: %v", err)
	}
}
