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
	"ramus/locales"
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

// A diff tool of git's that runs in a terminal, named for diffs or, by
// git difftool's fallback, for merges, is refused before it starts; one
// the user's config gives a command of its own runs as it is.
func TestTerminalDiffToolRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the tool is a shell command")
	}
	for _, c := range []struct {
		name    string
		config  [][2]string
		refused string
		// fails is set for a tool git does not know, which git is not
		// left to guess: its guess may be vimdiff, waiting unseen.
		fails bool
	}{
		{"diff.tool", [][2]string{{"diff.tool", "vimdiff"}}, "vimdiff", false},
		{"merge.tool", [][2]string{{"merge.tool", "nvimdiff2"}}, "nvimdiff2", false},
		{"diff.guitool", [][2]string{{"diff.tool", "meld"}, {"diff.guitool", "emerge"}}, "emerge", false},
		{"own command", [][2]string{{"diff.tool", "vimdiff"}, {"difftool.vimdiff.cmd", "true"}}, "", false},
		{"a vim with a window", [][2]string{{"diff.tool", "vimdiff"}, {"difftool.vimdiff.path", "@mvim"}}, "", false},
		{"unknown tool", [][2]string{{"diff.tool", "nosuchtool"}}, "", true},
		{"another case than its command's", [][2]string{{"diff.tool", "mine"}, {"difftool.Mine.cmd", "true"}}, "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := gittest.Init(t)
			r.Write("a.txt", "one\n")
			r.Commit("a")
			r.Write("a.txt", "two\n")
			// A stand-in for a vim with a window, which exits as one
			// closed would.
			bin := t.TempDir()
			if err := os.WriteFile(filepath.Join(bin, "mvim"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			for _, kv := range c.config {
				r.Git("config", kv[0], strings.Replace(kv[1], "@", bin+"/", 1))
			}
			m := make(mainThread, 64)
			s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
			t.Cleanup(func() { s.Close(); <-s.Done() })
			m.until(t, "the status", func() bool { return s.State.Status != nil })
			var got error
			done := false
			s.OpenDiffTool(DiffToolRequest{Paths: []string{"a.txt"}}, func(err error) { got, done = err, true })
			m.until(t, "the tool", func() bool { return done })
			if c.fails {
				if got == nil || strings.HasPrefix(got.Error(), "session:") {
					t.Fatalf("got %v, want git's error", got)
				}
				return
			}
			if c.refused == "" {
				if got != nil {
					t.Fatalf("the tool of the user's own command failed: %v", got)
				}
				return
			}
			if got == nil || got.Error() != locales.SessionDiffTerminalTool(c.refused) {
				t.Fatalf("got %v, want %s refused", got, c.refused)
			}
			if n := s.tools.open.Load(); n != 0 {
				t.Fatalf("%d tools counted open", n)
			}
		})
	}
}

// A diff tool open when Trust opens the repository again stays open,
// with what the user edited in it, and its end is told once it closes;
// closing the session (its tab) still stops it.
func TestDiffToolOutlivesTheReopen(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the tool is a shell command that traps its stop")
	}
	r := gittest.Init(t)
	r.Write("a.txt", "one\n")
	r.Commit("a")
	r.Write("a.txt", "two\n")
	dir := t.TempDir()
	signal := func(name string) string { return filepath.ToSlash(filepath.Join(dir, name)) }
	// The tool says it started, and that it was stopped, by the files it
	// writes; it runs until the test writes release.
	t.Cleanup(func() { os.WriteFile(signal("release"), nil, 0o644) })
	own := fmt.Sprintf(`trap 'echo > %q; exit 1' TERM; echo > %q; while [ ! -e %q ]; do sleep 0.05; done`,
		signal("stopped"), signal("started"), signal("release"))
	exists := func(name string) bool { _, err := os.Stat(filepath.Join(dir, name)); return err == nil }
	m := make(mainThread, 256)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "the status", func() bool { return s.State.Status != nil })

	var got error
	calls := 0
	s.OpenDiffTool(DiffToolRequest{Paths: []string{"a.txt"}, Own: own}, func(err error) { got = err; calls++ })
	m.until(t, "the tool to start", func() bool { return exists("started") })
	old := s.Repo
	s.reopenTrusted()
	// Every goroutine of the closed repository has ended: had the tool
	// run in its lifetime, it would have been stopped by now.
	m.drainClosed(t, old)
	m.until(t, "the repository open again", func() bool { return s.Repo != nil && s.State.Status != nil })
	if exists("stopped") || calls != 0 {
		t.Fatalf("the reopen ended the tool: stopped %v, done called %d times", exists("stopped"), calls)
	}
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m.until(t, "the tool's end", func() bool { return calls > 0 })
	if got != nil || calls != 1 {
		t.Fatalf("the tool's end told %d times, the last %v", calls, got)
	}

	// Closing the session stops a tool still open, and tells no one.
	for _, f := range []string{"started", "release"} {
		os.Remove(filepath.Join(dir, f))
	}
	calls = 0
	s.OpenDiffTool(DiffToolRequest{Paths: []string{"a.txt"}, Own: own}, func(error) { calls++ })
	m.until(t, "the second tool to start", func() bool { return exists("started") })
	s.Close()
	m.until(t, "the tool stopped", func() bool { return exists("stopped") })
	<-s.Done()
	for len(m) > 0 {
		(<-m)()
	}
	if calls != 0 {
		t.Fatalf("a tool the closing stopped told its end %d times", calls)
	}
}
