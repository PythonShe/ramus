package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"ramus/internal/git/gitexe"
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

// The tool, which may outlive the app and start terminals of its own,
// gets none of the app's askpass, nor its literal pathspecs; a path
// with glob characters still names only itself.
func TestDiffToolEnvironment(t *testing.T) {
	r := gittest.Init(t)
	odd := "a[1].txt"
	if !gittest.Portable(odd) {
		t.Skip("no brackets in names here")
	}
	r.Write(odd, "one\n")
	r.Write("a1.txt", "one\n")
	r.Commit("a")
	r.Write(odd, "two\n")
	r.Write("a1.txt", "two\n")
	dir := t.TempDir()
	env, seen := filepath.Join(dir, "env"), filepath.Join(dir, "seen")
	r.Git("config", "difftool.record.cmd", `env > `+filepath.ToSlash(env)+`; echo "$MERGED" >> `+filepath.ToSlash(seen))
	r.Git("config", "diff.tool", "record")
	g, err := gitexe.Find(t.Context(), os.Getenv("PATH"))
	if err != nil {
		t.Skip(err)
	}
	// The user's environment, as the app has it, sets no GIT_TERMINAL_PROMPT.
	runner := run.New(g, slices.DeleteFunc(gittest.Env(), func(e string) bool { return strings.HasPrefix(e, "GIT_TERMINAL_PROMPT=") }), 8)
	runner.SetPrompt([]string{"SSH_ASKPASS=/x/ramus", "SSH_ASKPASS_REQUIRE=force", "RAMUS_ASKPASS_TOKEN=tok"}, nil)
	m := make(mainThread, 64)
	s := Open(runner, trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "the status", func() bool { return s.State.Status != nil })
	var got error
	done := false
	s.OpenDiffTool(DiffToolRequest{Paths: []string{odd}}, func(err error) { got, done = err, true })
	m.until(t, "the tool", func() bool { return done })
	if got != nil {
		t.Fatal(got)
	}
	b, _ := os.ReadFile(env)
	for _, name := range []string{"SSH_ASKPASS", "RAMUS_ASKPASS", "GIT_LITERAL_PATHSPECS", "GIT_TERMINAL_PROMPT", "GIT_LFS_FORCE_PROGRESS"} {
		for line := range strings.SplitSeq(string(b), "\n") {
			if strings.HasPrefix(line, name) {
				t.Errorf("the tool has %s", line)
			}
		}
	}
	if b, _ := os.ReadFile(seen); string(b) != odd+"\n" {
		t.Fatalf("the tool saw %q", b)
	}
}

// A tool that leaves a process of its own running, as one that hands the
// files to a window already open does, ended well: git difftool exited
// 0, though what it started still holds its output.
func TestDiffToolLeavingAProcessRunning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the tool is a shell command")
	}
	r := gittest.Init(t)
	r.Write("a.txt", "one\n")
	r.Commit("a")
	r.Write("a.txt", "two\n")
	stop := filepath.Join(t.TempDir(), "stop")
	// The leftover ends once the test has, by the file it waits for.
	t.Cleanup(func() { os.WriteFile(stop, nil, 0o644) })
	own := fmt.Sprintf(`while [ ! -e %q ]; do sleep 0.1; done &`, filepath.ToSlash(stop))
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "the status", func() bool { return s.State.Status != nil })
	var got error
	done := false
	s.OpenDiffTool(DiffToolRequest{Paths: []string{"a.txt"}, Own: own}, func(err error) { got, done = err, true })
	m.until(t, "the tool", func() bool { return done })
	if got != nil {
		t.Fatalf("the tool failed: %v", got)
	}
}
