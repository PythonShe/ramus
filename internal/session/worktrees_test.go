package session

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/diff"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/repo"
)

// runFor runs what goes to the main thread for d.
func (m mainThread) runFor(d time.Duration) {
	end := time.After(d)
	for {
		select {
		case fn := <-m:
			fn()
		case <-end:
			return
		}
	}
}

// old sets the times of the files under dir back, as if no one had
// touched them for a while.
func old(t *testing.T, dir string) {
	t.Helper()
	then := time.Now().Add(-time.Hour)
	filepath.Walk(dir, func(p string, _ os.FileInfo, err error) error {
		if err == nil {
			os.Chtimes(p, then, then)
		}
		return nil
	})
}

// addWorktree adds a worktree of r on a new branch, in a folder of its
// own, and returns its path.
func addWorktree(t *testing.T, r *gittest.Repo, branch string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), branch)
	r.Git("worktree", "add", "-q", "-b", branch, dir)
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	return dir
}

// indexTime is when the index of the worktree at path was written.
func indexTime(t *testing.T, path string) time.Time {
	t.Helper()
	fi, err := os.Stat(filepath.Join(worktreeGitDir(path), "index"))
	if err != nil {
		t.Fatal(err)
	}
	return fi.ModTime()
}

// A worktree's summary counts its files and lines changed against HEAD,
// staged or not, untracked files whole, and says when it was last
// written and whether it is in use: just written, git holding its index,
// locked, or a merge stopped there.
func TestWorktreeSummary(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a.txt", "one\ntwo\nthree\n")
	r.Write("gone.txt", "x\n")
	r.Commit("one")
	wt := addWorktree(t, r, "agent")
	old(t, wt)
	old(t, r.GitDir())
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "the worktrees", func() bool { return len(s.State.Worktrees) == 2 })
	w := s.State.Worktrees[1]
	read := func() WorktreeSummary {
		t.Helper()
		var got *WorktreeSummary
		stop := s.SummarizeWorktrees(s.State.Worktrees[1:], func(sum WorktreeSummary) { got = &sum })
		defer stop()
		m.until(t, "the summary", func() bool { return got != nil })
		return *got
	}

	if sum := read(); sum.Err != nil || sum.Changes != 0 || sum.InUse || sum.Busy {
		t.Fatalf("a worktree left alone: %+v", sum)
	}

	os.WriteFile(filepath.Join(wt, "a.txt"), []byte("one\n2\nthree\nfour\n"), 0o644)
	os.Remove(filepath.Join(wt, "gone.txt"))
	os.MkdirAll(filepath.Join(wt, "new"), 0o755)
	os.WriteFile(filepath.Join(wt, "new", "b.txt"), []byte("b\nb\n"), 0o644)
	before := indexTime(t, wt)
	sum := read()
	if indexTime(t, wt) != before {
		t.Fatal("reading the worktree wrote its index")
	}
	if sum.Err != nil || sum.Changes != 3 || sum.Added != 4 || sum.Removed != 2 {
		t.Fatalf("summary %+v, want 3 files, +4 -2", sum)
	}
	paths := []string{}
	for _, f := range sum.Files {
		paths = append(paths, f.Path)
	}
	if strings.Join(paths, " ") != "a.txt gone.txt new/b.txt" || !sum.Files[1].Deleted || !sum.Files[2].Untracked || sum.Files[2].Added != 2 {
		t.Fatalf("files %+v", sum.Files)
	}
	if !sum.Busy || !sum.InUse || time.Since(sum.Active) > time.Minute {
		t.Fatalf("a worktree just written is not in use: %+v", sum)
	}

	old(t, wt)
	if sum := read(); sum.Busy || sum.InUse {
		t.Fatalf("a worktree left for an hour is in use: %+v", sum)
	}
	gitDir := worktreeGitDir(w.Path)
	os.WriteFile(filepath.Join(gitDir, "index.lock"), nil, 0o644)
	if sum := read(); !sum.Busy || !sum.InUse {
		t.Fatalf("git holds the index, yet %+v", sum)
	}
	stale := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(gitDir, "index.lock"), stale, stale)
	if sum := read(); sum.Busy {
		t.Fatalf("a lock left an hour ago keeps the worktree busy: %+v", sum)
	}
	os.Remove(filepath.Join(gitDir, "index.lock"))
	os.WriteFile(filepath.Join(gitDir, "MERGE_HEAD"), []byte(strings.Repeat("0", 40)+"\n"), 0o644)
	if sum := read(); sum.Busy || !sum.InUse {
		t.Fatalf("a merge stopped there: %+v", sum)
	}
}

// A live watch reads the worktrees again as they change, and the patch
// of a file there is against its HEAD.
func TestWatchWorktrees(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a.txt", "one\n")
	r.Commit("one")
	wt := addWorktree(t, r, "agent")
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	s.worktreeEvery = 10 * time.Millisecond
	m.until(t, "the worktrees", func() bool { return len(s.State.Worktrees) == 2 })
	// The worktree by the path git gives, as the app has it: on Windows
	// the temporary directory's may be a short name git writes long.
	path := s.State.Worktrees[1].Path
	var last WorktreeSummary
	stop := s.WatchWorktrees(s.State.Worktrees, func(sum WorktreeSummary) {
		if sum.Path == path {
			last = sum
		}
	})
	defer stop()
	m.until(t, "the first reading", func() bool { return last.Path != "" })
	os.WriteFile(filepath.Join(wt, "a.txt"), []byte("one\ntwo\n"), 0o644)
	m.until(t, "the change read again", func() bool { return last.Added == 1 })

	var got *PatchResult
	s.WorktreePatch(path, "a.txt", false, func(res PatchResult) { got = &res })
	m.until(t, "the patch", func() bool { return got != nil })
	if got.Err != nil || got.Parsed == nil || len(got.Parsed.Files) != 1 {
		t.Fatalf("patch %+v", got)
	}
}

// A session held writes nothing, the background fetch with it, until it
// is taken over.
func TestHoldWrites(t *testing.T) {
	origin := gittest.Init(t)
	origin.Write("a.txt", "a\n")
	origin.Commit("one")
	r := gittest.Init(t)
	r.Git("remote", "add", "origin", origin.Dir)
	r.Git("fetch", "-q", "origin")
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	s.HoldWrites()
	m.until(t, "the status", func() bool { return s.State.Status != nil })
	if s.Queue != nil || !s.State.Held {
		t.Fatal("a held session can write")
	}
	want := origin.Commit("two")
	fetched := func() bool {
		got, _ := r.Run(nil, "rev-parse", "refs/remotes/origin/main")
		return strings.TrimSpace(got) == want
	}
	s.SetBackgroundFetch(20*time.Millisecond, nil)
	if s.fetch.stop != nil {
		t.Fatal("a held session has a fetch due")
	}
	m.runFor(300 * time.Millisecond)
	if fetched() {
		t.Fatal("a held session fetched")
	}
	s.TakeOver()
	if s.Queue == nil || s.State.Held {
		t.Fatal("taken over, the session still cannot write")
	}
	s.SetBackgroundFetch(20*time.Millisecond, nil)
	m.until(t, "the fetch once taken over", fetched)
}

// The background fetch waits while git works in another worktree.
func TestFetchWaitsForBusyWorktrees(t *testing.T) {
	origin := gittest.Init(t)
	origin.Write("a.txt", "a\n")
	origin.Commit("one")
	r := gittest.Init(t)
	r.Git("remote", "add", "origin", origin.Dir)
	r.Git("fetch", "-q", "origin")
	r.Git("checkout", "-q", "-b", "main", "origin/main")
	wt := addWorktree(t, r, "agent")
	old(t, wt)
	old(t, r.GitDir())
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	s.fetchBusyWait = 20 * time.Millisecond
	m.until(t, "the status", func() bool { return s.State.Status != nil })
	lock := filepath.Join(worktreeGitDir(wt), "index.lock")
	os.WriteFile(lock, nil, 0o644)
	want := origin.Commit("two")
	fetched := func() bool {
		got, _ := r.Run(nil, "rev-parse", "refs/remotes/origin/main")
		return strings.TrimSpace(got) == want
	}
	s.SetBackgroundFetch(20*time.Millisecond, nil)
	// Put off more than once: it was due, and still held off.
	for deadline := time.Now().Add(20 * time.Second); s.fetch.waited.Load() < 3; m.pump(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the fetch was never put off")
		}
	}
	if fetched() {
		t.Fatal("fetched while git held another worktree's index")
	}
	os.Remove(lock)
	m.until(t, "the fetch once the worktree is quiet", fetched)
}

// A held session reads the patch of a change without writing the index,
// which git diff would refresh.
func TestHeldPatchLeavesTheIndex(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a.txt", "one\n")
	r.Write("same.txt", "same\n")
	r.Commit("one")
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	s.HoldWrites()
	m.until(t, "the status", func() bool { return s.State.Status != nil })
	r.Write("a.txt", "one\ntwo\n")
	old(t, r.Dir)
	// Touched, not changed: git diff would write its new times down.
	then := time.Now().Add(-30 * time.Minute)
	os.Chtimes(filepath.Join(r.Dir, "same.txt"), then, then)
	index := filepath.Join(r.GitDir(), "index")
	fi, _ := os.Stat(index)
	var got *PatchResult
	s.Patch(diff.PatchRequest{Source: diff.Unstaged, Paths: []string{"a.txt", "same.txt"}}, Sides{}, func(res PatchResult) { got = &res })
	m.until(t, "the patch", func() bool { return got != nil })
	if got.Err != nil || got.Parsed == nil || len(got.Parsed.Hunks) != 1 {
		t.Fatalf("patch %+v", got)
	}
	if after, _ := os.Stat(index); !after.ModTime().Equal(fi.ModTime()) {
		t.Fatal("reading the patch wrote the index")
	}
}

// markWith returns a shell command that leaves a file named name in
// marks, for a driver that must not run.
func markWith(t *testing.T, marks, name string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the drivers are shell commands")
	}
	return fmt.Sprintf("touch %q; ", filepath.Join(marks, name))
}

// noMarks fails the test for every driver that ran.
func noMarks(t *testing.T, marks string) {
	t.Helper()
	if ran, _ := os.ReadDir(marks); len(ran) > 0 {
		var names []string
		for _, e := range ran {
			names = append(names, e.Name())
		}
		t.Errorf("drivers ran: %v", names)
	}
}

// readWorktree summarizes the worktree at path, and reads the patch of
// one of its files.
func readWorktree(t *testing.T, s *Session, m mainThread, path, file string) (WorktreeSummary, PatchResult) {
	t.Helper()
	var wt *repo.Worktree
	m.until(t, "the worktree listed", func() bool {
		for i, w := range s.State.Worktrees {
			if w.Path == path {
				wt = &s.State.Worktrees[i]
			}
		}
		return wt != nil
	})
	var sum *WorktreeSummary
	stop := s.SummarizeWorktrees([]repo.Worktree{*wt}, func(got WorktreeSummary) { sum = &got })
	defer stop()
	m.until(t, "the summary", func() bool { return sum != nil })
	var patch *PatchResult
	s.WorktreePatch(path, file, false, func(res PatchResult) { patch = &res })
	m.until(t, "the patch", func() bool { return patch != nil })
	return *sum, *patch
}

// A repository can list another repository as one of its worktrees; git
// there follows that repository's own config, which the user trusted in
// neither case, so its drivers do not run.
func TestWorktreeOfAnotherRepositoryRunsNoDriver(t *testing.T) {
	for _, trust := range []bool{false, true} {
		t.Run(fmt.Sprint("trusted=", trust), func(t *testing.T) {
			marks := t.TempDir()
			mark := markWith(t, marks, "other")
			r := gittest.Init(t)
			r.Write("a.txt", "one\n")
			head := r.Commit("one")
			other := gittest.Init(t)
			other.Write(".gitattributes", "*.y filter=other diff=other\n")
			other.Write("a.y", "one\n")
			other.Commit("one")
			other.Git("config", "filter.other.process", mark+"exit 1")
			other.Git("config", "filter.other.clean", mark+"cat")
			other.Git("config", "filter.other.required", "true")
			other.Git("config", "diff.other.textconv", mark+"cat")
			other.Write("a.y", "two\n")
			adm := filepath.Join(r.GitDir(), "worktrees", "x")
			os.MkdirAll(adm, 0o755)
			os.WriteFile(filepath.Join(adm, "gitdir"), []byte(filepath.Join(other.Dir, ".git")+"\n"), 0o644)
			os.WriteFile(filepath.Join(adm, "HEAD"), []byte(head+"\n"), 0o644)
			os.WriteFile(filepath.Join(adm, "commondir"), []byte("../..\n"), 0o644)
			var dirs []string
			if trust {
				dirs = append(dirs, r.GitDir())
			}
			m := make(mainThread, 64)
			s := Open(testrun.Runner(t), trusted(t, dirs...), r.Dir, m.update)
			t.Cleanup(func() { s.Close(); <-s.Done() })
			path, _ := filepath.EvalSymlinks(other.Dir)
			sum, patch := readWorktree(t, s, m, path, "a.y")
			noMarks(t, marks)
			if sum.Err != nil || sum.Changes != 1 || patch.Err != nil {
				t.Fatalf("summary %+v, patch error %v", sum, patch.Err)
			}
		})
	}
}

// A submodule is a repository of its own, which trusting the
// superproject does not trust: reading its commits runs nothing its
// config names.
func TestSubmoduleRangeRunsNoDriver(t *testing.T) {
	marks := t.TempDir()
	mark := markWith(t, marks, "gpg")
	// git runs gpg.program itself, not through the shell.
	gpg := filepath.Join(t.TempDir(), "gpg")
	os.WriteFile(gpg, []byte("#!/bin/sh\n"+mark+"exit 1\n"), 0o755)
	sub := gittest.Init(t)
	sub.Write("s", "0")
	old := sub.Commit("first")
	r := gittest.Init(t)
	r.Write("a.txt", "one\n")
	r.Commit("one")
	r.Git("-c", "protocol.file.allow=always", "submodule", "add", "-q", sub.Dir, "lib")
	r.Commit("with lib")
	inner := gittest.Open(t, filepath.Join(r.Dir, "lib"))
	inner.Write("s", "1")
	inner.Commit("second")
	// Signed, so that showing signatures starts the verifier.
	body := inner.Git("cat-file", "commit", "HEAD")
	head, rest, _ := strings.Cut(body, "\n")
	signed := head + "\n" + strings.Replace(rest, "\n\n", "\ngpgsig -----BEGIN PGP SIGNATURE-----\n \n -----END PGP SIGNATURE-----\n\n", 1)
	id, err := inner.Run([]byte(signed), "hash-object", "-t", "commit", "-w", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	inner.Git("config", "log.showSignature", "true")
	inner.Git("config", "gpg.program", gpg)
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "the status", func() bool { return s.State.Status != nil })
	var got *SubmoduleRange
	oldID, _ := hex.DecodeString(old)
	newID, _ := hex.DecodeString(strings.TrimSpace(id))
	s.LoadSubmoduleRange("lib", oldID, newID, func(res SubmoduleRange) { got = &res })
	m.until(t, "the range", func() bool { return got != nil })
	noMarks(t, marks)
	if got.Err != nil || len(got.Added) != 1 || got.Added[0].Subject != "second" {
		t.Fatalf("range %+v", got)
	}
}

// A linked worktree's own config.worktree is not in the hardening of the
// worktree the repository was opened in: read from a repository not
// trusted, its drivers do not run.
func TestLinkedWorktreeConfigRunsNoDriver(t *testing.T) {
	marks := t.TempDir()
	mark := markWith(t, marks, "wt")
	r := gittest.Init(t)
	r.Write(".gitattributes", "*.z filter=wt diff=wt\n")
	r.Write("a.z", "one\n")
	r.Commit("one")
	r.Git("config", "extensions.worktreeConfig", "true")
	wt := addWorktree(t, r, "agent")
	wtRepo := gittest.Open(t, wt)
	wtRepo.Git("config", "--worktree", "filter.wt.process", mark+"exit 1")
	wtRepo.Git("config", "--worktree", "filter.wt.clean", mark+"cat")
	wtRepo.Git("config", "--worktree", "filter.wt.required", "true")
	wtRepo.Git("config", "--worktree", "diff.wt.textconv", mark+"cat")
	os.WriteFile(filepath.Join(wt, "a.z"), []byte("two\n"), 0o644)
	s, m := openFor(t, r)
	if !s.State.Restricted {
		t.Fatal("the repository is trusted")
	}
	sum, patch := readWorktree(t, s, m, wt, "a.z")
	noMarks(t, marks)
	if sum.Err != nil || sum.Changes != 1 || patch.Err != nil {
		t.Fatalf("summary %+v, patch error %v", sum, patch.Err)
	}
}
