package snapshot

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/catfile"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/refs"
	"ramus/internal/git/run"
	"ramus/internal/git/tree"
)

type fixture struct {
	t   *testing.T
	r   *gittest.Repo
	run *run.Runner
	w   Where
}

func setup(t *testing.T) *fixture {
	r := gittest.Init(t)
	return &fixture{t: t, r: r, run: testrun.Runner(t), w: Where{GitDir: r.GitDir(), WorkTree: r.Dir}}
}

func (f *fixture) list(gitDir, common string) ([]Snapshot, *catfile.Process) {
	f.t.Helper()
	cat, err := catfile.Start(context.Background(), f.run, gitDir)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { cat.Close() })
	l, err := List(refs.New(gitDir, common, 20), cat)
	if err != nil {
		f.t.Fatal(err)
	}
	return l, cat
}

func (f *fixture) take(o Options) Snapshot {
	f.t.Helper()
	s, err := Take(context.Background(), f.run, f.w, o)
	if err != nil {
		f.t.Fatal(err)
	}
	return s
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTakeListRestore(t *testing.T) {
	f := setup(t)
	f.r.Write("a", "a1")
	f.r.Write("b", "b1")
	f.r.Write("dir/d", "d1")
	f.r.Commit("one")
	f.r.Write("a", "a2")
	f.r.Stage("b", "b2")
	f.r.Write("c", "untracked")
	f.r.Write("dir/d", "d2")
	f.r.Write("odd\nname", "n")
	status := f.r.Git("status", "--porcelain=v2", "-z")
	index := read(t, filepath.Join(f.w.GitDir, "index"))

	s := f.take(Options{Reason: "Discard\nchanges", Paths: []string{"a", "c", "dir", "odd\nname"}})
	if s.Head == "" || s.Index == "" || s.All {
		t.Fatalf("snapshot %+v", s)
	}
	if f.r.Git("status", "--porcelain=v2", "-z") != status || read(t, filepath.Join(f.w.GitDir, "index")) != index {
		t.Fatal("taking a snapshot changed the index or the work tree")
	}
	if ls, _ := os.ReadDir(f.w.GitDir); slices.ContainsFunc(ls, func(e os.DirEntry) bool { return strings.HasPrefix(e.Name(), "ramus-snapshot-") }) {
		t.Fatal("temporary index left behind")
	}

	list, cat := f.list(f.w.GitDir, f.w.GitDir)
	if len(list) != 1 {
		t.Fatalf("list %+v", list)
	}
	got := list[0]
	if got.Commit != s.Commit || got.Tree != s.Tree || got.Head != s.Head || got.Index != s.Index ||
		got.Reason != "Discard changes" || !slices.Equal(got.Paths, s.Paths) {
		t.Fatalf("listed %+v, took %+v", got, s)
	}
	files, err := Files(cat, 20, got)
	if err != nil {
		t.Fatal(err)
	}
	want := []File{{"a", tree.Modified}, {"c", tree.Added}, {"dir/d", tree.Modified}, {"odd\nname", tree.Added}}
	if !slices.Equal(files, want) {
		t.Fatalf("files %+v, want %+v", files, want)
	}
	if idx := f.r.Git("ls-tree", "--name-only", "-r", s.Index); !strings.Contains(idx, "b") || f.r.Git("show", s.Index+":b") != "b2" {
		t.Fatalf("index commit lost the staged b: %q", idx)
	}

	// The action the snapshot was for, then the way back.
	f.r.Git("checkout", "--", "a", "dir")
	os.Remove(filepath.Join(f.r.Dir, "c"))
	f.r.Write("a", "a3")
	before, err := Restore(context.Background(), f.run, f.w, got, []string{"a", "c", "dir/d"})
	if err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]string{"a": "a2", "c": "untracked", "dir/d": "d2"} {
		if s := read(t, filepath.Join(f.r.Dir, p)); s != want {
			t.Fatalf("%s restored as %q, want %q", p, s, want)
		}
	}
	if f.r.Git("show", before.Commit+":a") != "a3" {
		t.Fatal("restore did not keep what it overwrote")
	}
	if f.r.Git("diff", "--cached", "--name-only") != "b\n" {
		t.Fatalf("restore touched the index: %q", f.r.Git("diff", "--cached", "--name-only"))
	}
	if list, _ = f.list(f.w.GitDir, f.w.GitDir); len(list) != 2 || list[0].Commit != before.Commit {
		t.Fatalf("after restore %+v", list)
	}
}

func TestWholeTreeAndIgnored(t *testing.T) {
	f := setup(t)
	f.r.Write(".gitignore", "*.log\n")
	f.r.Commit("one")
	f.r.Write("x.log", "log")
	f.r.Write("new", "n")
	plain := f.take(Options{Reason: "Snapshot now"})
	all := f.take(Options{Reason: "Snapshot now", Ignored: true})
	list, cat := f.list(f.w.GitDir, f.w.GitDir)
	if len(list) != 2 || !list[0].Ignored || !list[0].All || list[1].Ignored || list[0].Commit != all.Commit || list[1].Commit != plain.Commit {
		t.Fatalf("list %+v", list)
	}
	names := func(s Snapshot) []string {
		fs, err := Files(cat, 20, s)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, f := range fs {
			out = append(out, f.Path)
		}
		return out
	}
	if got := names(list[1]); !slices.Equal(got, []string{"new"}) {
		t.Fatalf("plain %q", got)
	}
	if got := names(list[0]); !slices.Equal(got, []string{"new", "x.log"}) {
		t.Fatalf("with ignored %q", got)
	}
}

func TestUnbornAndConflicted(t *testing.T) {
	f := setup(t)
	f.r.Write("a", "1")
	s := f.take(Options{Reason: "unborn", Paths: []string{"a"}})
	if s.Head != "" || f.r.Git("show", s.Commit+":a") != "1" {
		t.Fatalf("unborn %+v", s)
	}

	f.r.Commit("base")
	f.r.Git("checkout", "-q", "-b", "side")
	f.r.Write("a", "side")
	f.r.Commit("side")
	f.r.Git("checkout", "-q", "main")
	f.r.Write("a", "main")
	f.r.Commit("main")
	if _, err := f.r.Run(nil, "merge", "side"); err == nil {
		t.Fatal("merge did not conflict")
	}
	s = f.take(Options{Reason: "conflict", Paths: []string{"a"}})
	if s.Index != "" || !strings.Contains(f.r.Git("show", s.Commit+":a"), "<<<<<<<") {
		t.Fatalf("conflicted %+v", s)
	}
	if !strings.Contains(f.r.Git("status", "--porcelain"), "UU a") {
		t.Fatal("snapshot resolved the conflict")
	}
}

func TestSurvivesGCAndExpires(t *testing.T) {
	f := setup(t)
	f.r.Write("a", "1")
	f.r.Commit("one")
	f.r.Write("a", "kept")
	s := f.take(Options{Reason: "x", Paths: []string{"a"}})
	f.r.Git("reflog", "expire", "--expire=now", "--all")
	f.r.Git("gc", "-q", "--prune=now")
	if f.r.Git("show", s.Commit+":a") != "kept" {
		t.Fatal("gc pruned the snapshot")
	}
	list, _ := f.list(f.w.GitDir, f.w.GitDir)
	n, err := Expire(context.Background(), f.run, f.w, list, time.Now().Add(-time.Hour))
	if err != nil || n != 0 {
		t.Fatalf("expired %d young snapshots: %v", n, err)
	}
	n, err = Expire(context.Background(), f.run, f.w, list, time.Now().Add(time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("expire %d %v", n, err)
	}
	if list, _ = f.list(f.w.GitDir, f.w.GitDir); len(list) != 0 {
		t.Fatalf("left %+v", list)
	}
}

func TestPerWorktree(t *testing.T) {
	f := setup(t)
	f.r.Write("a", "1")
	f.r.Commit("one")
	wt := filepath.Join(t.TempDir(), "wt")
	f.r.Git("worktree", "add", "-q", "-b", "other", wt)
	wtGit := strings.TrimSpace(f.r.Git("-C", wt, "rev-parse", "--absolute-git-dir"))
	os.WriteFile(filepath.Join(wt, "a"), []byte("in worktree"), 0o644)
	f.w = Where{GitDir: wtGit, WorkTree: wt}
	s := f.take(Options{Reason: "wt", Paths: []string{"a"}})
	if l, _ := f.list(wtGit, f.r.GitDir()); len(l) != 1 || l[0].Commit != s.Commit {
		t.Fatalf("worktree list %+v", l)
	}
	if l, _ := f.list(f.r.GitDir(), f.r.GitDir()); len(l) != 0 {
		t.Fatalf("main worktree sees %+v", l)
	}
	if !bytes.Contains([]byte(f.r.Git("-C", wt, "show", s.Commit+":a")), []byte("in worktree")) {
		t.Fatal("wrong content")
	}
}
