package session

import (
	"errors"
	"fmt"
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
	// Without a diff tool, git difftool runs the merge tool.
	r.Git("config", "--unset", "diff.tool")
	r.Git("config", "mergetool.record.cmd", `true`)
	r.Git("config", "merge.tool", "record")
	os.Remove(out)
	if err := run1(open(true), DiffToolRequest{Paths: []string{"a.txt"}, From: head, To: next}); err != nil {
		t.Fatalf("with a merge tool only: %v", err)
	}
	if b, _ := os.ReadFile(out); string(b) != "one\ntwo\n" {
		t.Fatalf("with a merge tool only the tool saw %q", b)
	}
	r.Git("config", "--unset", "merge.tool")
	// None set: the app's own, for this run only.
	os.Remove(out)
	own := `cat "$LOCAL" "$REMOTE" > ` + filepath.ToSlash(out)
	if err := run1(open(true), DiffToolRequest{Paths: []string{"a.txt"}, From: head, To: next, Own: own}); err != nil {
		t.Fatalf("the app's own: %v", err)
	}
	if b, _ := os.ReadFile(out); string(b) != "one\ntwo\n" {
		t.Fatalf("the app's own saw %q", b)
	}
	if got := r.Git("config", "--get-regexp", "difftool"); strings.Contains(got, "ramus") {
		t.Fatalf("the app's own left in the config: %s", got)
	}
	if err := run1(open(true), DiffToolRequest{Paths: []string{"a.txt"}}); !errors.Is(err, ErrNoDiffTool) {
		t.Fatalf("no tool: %v", err)
	}
}

// More files than a command line names on Windows go to the tool in
// several runs of git difftool, every file once.
func TestDiffToolManyFiles(t *testing.T) {
	r := gittest.Init(t)
	r.Git("config", "core.longpaths", "true")
	// Few files, as each opens the tool: long names fill the line.
	long := strings.Repeat("a folder with a long name ", 8)
	var paths []string
	for i := range 40 {
		paths = append(paths, fmt.Sprintf("%s/%s/%s/file %03d.txt", long, long, long, i))
		r.Write(paths[i], "one\n")
	}
	r.Commit("a")
	for _, p := range paths {
		r.Write(p, "two\n")
	}
	out := filepath.Join(t.TempDir(), "seen")
	r.Git("config", "difftool.record.cmd", `echo "$BASE" >> `+filepath.ToSlash(out))
	r.Git("config", "diff.tool", "record")
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "the status", func() bool { return s.State.Status != nil })
	done := false
	s.OpenDiffTool(DiffToolRequest{Paths: paths}, func(err error) {
		if err != nil {
			t.Error(err)
		}
		done = true
	})
	m.until(t, "the tool", func() bool { return done })
	if b, _ := os.ReadFile(out); strings.Count(string(b), "\n") != len(paths) {
		t.Fatalf("the tool saw %d files of %d", strings.Count(string(b), "\n"), len(paths))
	}
	for _, rec := range s.Repo.Runner.Journal().For(r.GitDir()) {
		if len(rec.Line) > 24<<10+1024 {
			t.Fatalf("a command line of %d characters", len(rec.Line))
		}
	}
}
