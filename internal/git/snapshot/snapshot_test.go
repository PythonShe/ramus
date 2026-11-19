package snapshot

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/catfile"
	"ramus/internal/git/gitexe"
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
	l, err := List(context.Background(), refs.New(gitDir, common, 20), cat)
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
	odd := "odd\nname"
	if !gittest.Portable(odd) {
		odd = "odd name"
	}
	f.r.Write(odd, "n")
	status := f.r.Git("status", "--porcelain=v2", "-z")
	index := read(t, filepath.Join(f.w.GitDir, "index"))

	s := f.take(Options{Reason: "Discard\nchanges", Paths: []string{"a", "c", "dir", odd}})
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
	// A read stopped, as when the repository closes, stops listing.
	stopped, stop := context.WithCancel(context.Background())
	stop()
	if _, err := List(stopped, refs.New(f.w.GitDir, f.w.GitDir, 20), cat); !errors.Is(err, context.Canceled) {
		t.Fatalf("list stopped: %v", err)
	}
	if _, err := Files(stopped, cat, 20, got); !errors.Is(err, context.Canceled) {
		t.Fatalf("files stopped: %v", err)
	}
	files, err := Files(context.Background(), cat, 20, got)
	if err != nil {
		t.Fatal(err)
	}
	want := []File{{"a", tree.Modified}, {"c", tree.Added}, {"dir/d", tree.Modified}, {odd, tree.Added}}
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
	before, err := Restore(context.Background(), f.run, f.w, got, []string{"a", "c", "dir/d"}, Overlay)
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
		fs, err := Files(context.Background(), cat, 20, s)
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
	// Other files are kept while a conflict sits outside the paths, and
	// the whole tree is kept with one in it.
	f.r.Write("other", "x")
	s = f.take(Options{Reason: "beside a conflict", Paths: []string{"other"}})
	if f.r.Git("show", s.Commit+":other") != "x" {
		t.Fatalf("beside a conflict %+v", s)
	}
	s = f.take(Options{Reason: "whole tree"})
	if !strings.Contains(f.r.Git("show", s.Commit+":a"), "<<<<<<<") || !strings.Contains(f.r.Git("status", "--porcelain"), "UU a") {
		t.Fatalf("whole tree %+v", s)
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

// Delete removes the snapshots asked for and leaves the others; one that
// is no longer what was listed, or a ref that is no snapshot, removes
// nothing.
func TestDelete(t *testing.T) {
	f := setup(t)
	f.r.Write("a", "1")
	f.r.Commit("one")
	f.r.Write("a", "2")
	first := f.take(Options{Reason: "first", Paths: []string{"a"}})
	f.r.Write("a", "3")
	second := f.take(Options{Reason: "second", Paths: []string{"a"}})
	ctx := context.Background()
	stale := first
	stale.Commit = second.Commit
	if err := Delete(ctx, f.run, f.w, []Snapshot{second, stale}); err == nil {
		t.Fatal("a snapshot no longer as listed deleted")
	}
	branch := Snapshot{Ref: "refs/heads/main", Commit: first.Commit}
	if err := Delete(ctx, f.run, f.w, []Snapshot{branch}); err == nil {
		t.Fatal("a branch deleted as a snapshot")
	}
	if list, _ := f.list(f.w.GitDir, f.w.GitDir); len(list) != 2 {
		t.Fatalf("a failed delete left %d", len(list))
	}
	if err := Delete(ctx, f.run, f.w, []Snapshot{first}); err != nil {
		t.Fatal(err)
	}
	list, _ := f.list(f.w.GitDir, f.w.GitDir)
	if len(list) != 1 || list[0].Reason != "second" {
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

// Removing a worktree keeps the snapshots taken in it, under the worktree
// that removes it, besides its state as it was.
func TestRemovedWorktreeKeepsItsSnapshots(t *testing.T) {
	f := setup(t)
	f.r.Write("a", "1")
	f.r.Commit("one")
	main := f.w
	wt, _ := filepath.EvalSymlinks(t.TempDir())
	wt = filepath.Join(wt, "wt")
	f.r.Git("worktree", "add", "-q", "--detach", wt)
	wtGit := strings.TrimSpace(f.r.Git("-C", wt, "rev-parse", "--absolute-git-dir"))
	os.WriteFile(filepath.Join(wt, "a"), []byte("precious"), 0o644)
	f.w = Where{GitDir: wtGit, WorkTree: wt}
	old := f.take(Options{Reason: "Discard changes to a", Paths: []string{"a"}})
	f.r.Git("-C", wt, "checkout", "--", "a")
	f.w = main
	// Twice, as when the first removal failed after its snapshot.
	for range 2 {
		f.take(Options{Reason: "Remove worktree wt", Ignored: true, Worktree: wt})
	}
	f.r.Git("worktree", "remove", "--force", wt)
	l, _ := f.list(f.r.GitDir(), f.r.GitDir())
	if !slices.ContainsFunc(l, func(s Snapshot) bool { return s.Commit == old.Commit && s.Ref == old.Ref }) {
		t.Fatalf("the worktree's snapshot is gone: %+v", l)
	}
	if len(l) != 3 {
		t.Errorf("%d snapshots, want the worktree's and two of its removal", len(l))
	}
}

// A worktree whose folder was deleted by hand has no files left to keep,
// but its snapshots are still in its git directory until git forgets it:
// they are kept under the worktree that removes it.
func TestWorktreeFolderGoneKeepsItsSnapshots(t *testing.T) {
	for _, relative := range []bool{false, true} {
		f := setup(t)
		f.r.Write("a", "1")
		f.r.Commit("one")
		main := f.w
		wt, _ := filepath.EvalSymlinks(t.TempDir())
		wt = filepath.Join(wt, "wt")
		if relative {
			// As git 2.48 and later write it with worktree.useRelativePaths.
			f.r.Git("config", "worktree.useRelativePaths", "true")
		}
		f.r.Git("worktree", "add", "-q", "--detach", wt)
		wtGit := strings.TrimSpace(f.r.Git("-C", wt, "rev-parse", "--absolute-git-dir"))
		os.WriteFile(filepath.Join(wt, "a"), []byte("precious"), 0o644)
		f.w = Where{GitDir: wtGit, WorkTree: wt}
		old := f.take(Options{Reason: "Discard changes to a", Paths: []string{"a"}})
		if err := os.RemoveAll(wt); err != nil {
			t.Fatal(err)
		}
		f.w = main
		if s := f.take(Options{Reason: "Remove worktree wt", Ignored: true, Worktree: wt}); s.Commit != "" {
			t.Fatalf("a snapshot of a folder that is gone: %+v", s)
		}
		f.r.Git("worktree", "prune", "--expire=now")
		if _, err := os.Stat(wtGit); err == nil {
			t.Fatal("git did not forget the worktree")
		}
		l, _ := f.list(f.r.GitDir(), f.r.GitDir())
		if len(l) != 1 || l[0].Commit != old.Commit || l[0].Ref != old.Ref {
			t.Fatalf("relative %v: the worktree's snapshot is gone: %+v", relative, l)
		}
	}
}

// A worktree whose gitdir file is missing, empty or cannot be read, and
// not locked, is one git worktree prune removes, as it does one whose
// folder is gone: its snapshots are kept first. A locked one, and one
// still there, are left as they are.
func TestKeepPrunedWithoutGitdirFile(t *testing.T) {
	f := setup(t)
	f.r.Write("a", "1")
	f.r.Commit("one")
	base, _ := filepath.EvalSymlinks(t.TempDir())
	names := []string{"missing", "empty", "unreadable", "locked", "there"}
	for _, n := range names {
		wt := filepath.Join(base, n)
		f.r.Git("worktree", "add", "-q", "--detach", wt)
		f.r.Git("-C", wt, "update-ref", Prefix+n, "HEAD")
	}
	admin := func(n string) string { return filepath.Join(f.r.GitDir(), "worktrees", n, "gitdir") }
	f.r.Git("worktree", "lock", filepath.Join(base, "locked"))
	for _, n := range []string{"missing", "locked"} {
		if err := os.Remove(admin(n)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(admin("empty"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	want := []string{"empty", "missing"}
	if err := os.Chmod(admin("unreadable"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(admin("unreadable"), 0o644) })
	if _, err := os.ReadFile(admin("unreadable")); err == nil {
		t.Log("the gitdir file is still readable here (as root, or on Windows): that case is left out")
		os.Chmod(admin("unreadable"), 0o644)
	} else {
		want = append(want, "unreadable")
	}
	if err := KeepPruned(context.Background(), f.run, f.w); err != nil {
		t.Fatal(err)
	}
	f.r.Git("worktree", "prune")
	for _, n := range want {
		if _, err := os.Stat(filepath.Dir(admin(n))); err == nil {
			t.Fatalf("git did not prune %s: the test shows nothing", n)
		}
	}
	var kept []string
	for _, s := range strings.Fields(f.r.Git("for-each-ref", "--format=%(refname)", Prefix)) {
		kept = append(kept, strings.TrimPrefix(s, Prefix))
	}
	slices.Sort(want)
	if !slices.Equal(kept, want) {
		t.Fatalf("snapshots kept %q, want %q", kept, want)
	}
}

// An index copy a snapshot left when the app ended in the middle of it is
// removed by the next snapshot; one a snapshot may be using stays.
func TestLeftIndexCopiesRemoved(t *testing.T) {
	f := setup(t)
	f.r.Write("a", "1")
	f.r.Commit("one")
	gd := f.r.GitDir()
	long := time.Now().Add(-48 * time.Hour)
	left := []string{
		copyPrefix + strconv.FormatInt(long.UnixNano(), 10) + "-00.index",
		copyPrefix + strconv.FormatInt(long.UnixNano(), 10) + "-00.index.lock",
		copyPrefix + "0123456789abcdef.index", // named before the time was in it
	}
	using := copyPrefix + strconv.FormatInt(time.Now().UnixNano(), 10) + "-01.index"
	for _, n := range append(slices.Clone(left), using) {
		os.WriteFile(filepath.Join(gd, n), []byte("x"), 0o600)
	}
	os.Chtimes(filepath.Join(gd, left[2]), long, long)
	f.take(Options{Reason: "x"})
	for _, n := range left {
		if _, err := os.Lstat(filepath.Join(gd, n)); err == nil {
			t.Errorf("%s left", n)
		}
	}
	if _, err := os.Lstat(filepath.Join(gd, using)); err != nil {
		t.Errorf("a copy in use removed: %v", err)
	}
}

func TestAssumeUnchanged(t *testing.T) {
	f := setup(t)
	f.r.Write("a", "1")
	f.r.Commit("one")
	f.r.Git("update-index", "--assume-unchanged", "a")
	f.r.Write("a", "hidden from status")
	s := f.take(Options{Reason: "x", Paths: []string{"a"}})
	if f.r.Git("show", s.Commit+":a") != "hidden from status" {
		t.Fatal("snapshot skipped an assume-unchanged file")
	}
	if !strings.HasPrefix(f.r.Git("ls-files", "-v", "a"), "h ") {
		t.Fatal("the real index lost its mark")
	}
}

func TestRestoreModes(t *testing.T) {
	f := setup(t)
	f.r.Write("dir/a", "a1")
	f.r.Write("b", "b1")
	f.r.Write("gone", "g")
	f.r.Commit("one")
	f.r.Stage("b", "b2")
	f.r.Write("b", "b3")
	os.Remove(filepath.Join(f.r.Dir, "gone"))
	s := f.take(Options{Reason: "x", Paths: []string{"dir", "b", "gone"}})

	// Overlay: a file added since stays, and nothing is deleted.
	f.r.Write("dir/new", "added later")
	f.r.Write("dir/a", "changed")
	f.r.Git("checkout", "--", "gone")
	if _, err := Restore(context.Background(), f.run, f.w, s, []string{"dir"}, Overlay); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(f.r.Dir, "dir/a")) != "a1" || read(t, filepath.Join(f.r.Dir, "dir/new")) != "added later" {
		t.Fatal("overlay restore")
	}

	// Exact: what a hard reset dropped comes back, staged and unstaged,
	// a file that was deleted is deleted again, and one the snapshot did
	// not have, made since, goes.
	f.r.Git("reset", "-q", "--hard")
	paths := []string{"dir", "b", "gone"}
	made, err := FindMade(context.Background(), f.run, f.w, s, paths)
	if err != nil || !made.Known || len(made.Files) != 1 || made.Files[0].Path != "dir/new" {
		t.Fatalf("made %+v %v", made, err)
	}
	if _, left, err := RestoreExact(context.Background(), f.run, f.w, s, paths, made); err != nil || len(left) > 0 {
		t.Fatal(left, err)
	}
	if f.r.Git("show", ":b") != "b2" || read(t, filepath.Join(f.r.Dir, "b")) != "b3" {
		t.Fatalf("staged %q, work tree %q", f.r.Git("show", ":b"), read(t, filepath.Join(f.r.Dir, "b")))
	}
	if _, err := os.Stat(filepath.Join(f.r.Dir, "gone")); err == nil {
		t.Fatal("a deleted file came back")
	}
	if _, err := os.Lstat(filepath.Join(f.r.Dir, "dir/new")); err == nil {
		t.Fatal("a file the snapshot did not have was left")
	}
}

// Many paths restored are found in the snapshot by one listing of its
// tree, not one listing of the whole tree per handful of paths.
func TestRestoreManyPathsListsOnce(t *testing.T) {
	f := setup(t)
	var paths []string
	for i := range 3*maxPathspecs + 1 {
		p := "d" + strconv.Itoa(i%7) + "/f" + strconv.Itoa(i)
		f.r.Write(p, "1")
		paths = append(paths, p)
	}
	f.r.Commit("one")
	for _, p := range paths[:10] {
		f.r.Write(p, "changed")
	}
	s := f.take(Options{Reason: "x", Paths: paths})
	f.r.Git("checkout", "--", ".")
	from := len(f.run.Journal().For(f.w.GitDir))
	if _, err := Restore(context.Background(), f.run, f.w, s, paths, Exact); err != nil {
		t.Fatal(err)
	}
	trees := 0
	for _, rec := range f.run.Journal().For(f.w.GitDir)[from:] {
		if strings.Contains(rec.Line, " ls-tree ") {
			trees++
		}
	}
	if trees > 2 {
		t.Errorf("%d listings of a tree, want one for the files and one for the index", trees)
	}
	if read(t, filepath.Join(f.r.Dir, paths[3])) != "changed" || read(t, filepath.Join(f.r.Dir, paths[20])) != "1" {
		t.Fatal("not restored")
	}
}

// An exact restore removes the files a write made, as FindMade read them
// once it ran, a folder of them with them, ignored ones too, and only
// those, each only as the write left it: an ignored file there before,
// which the snapshot did not keep, stays though a .gitignore the write
// made stopped ignoring it; a file made since, ignored by a rule the
// write made or not, stays; a made file changed since stays, and is
// named. Without what the write made, nothing is removed.
func TestExactRemovesOnlyWhatWasNotThere(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.r.Write(".gitignore", "*.log\n")
	f.r.Write("dir/a", "a")
	f.r.Commit("one")
	f.r.Write("dir/keep.log", "ignored, there before")
	f.r.Write("dir/untracked", "there before")
	paths := []string{"dir", "new", "deep"}
	s := f.take(Options{Reason: "x", Paths: paths})

	// What the write made.
	f.r.Write("new", "made")
	f.r.Write("deep/er/file", "made")
	f.r.Write("dir/made", "made")
	f.r.Write("dir/edited", "made")
	f.r.Write("dir/.gitignore", "!keep.log\nsecret.env\n")
	f.r.Write("dir/made.log", "made, ignored")
	if err := os.Symlink("a", filepath.Join(f.r.Dir, "dir", "link")); err != nil {
		t.Skip(err)
	}
	made, err := FindMade(ctx, f.run, f.w, s, paths)
	if err != nil || !made.Known {
		t.Fatal(made, err)
	}
	// What the user did since.
	f.r.Write("dir/secret.env", "the user's, ignored by the write's rule")
	f.r.Write("dir/mine", "the user's")
	f.r.Write("dir/edited", "made, then edited")

	if _, err := Restore(ctx, f.run, f.w, s, paths, Exact); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(f.r.Dir, "new")); err != nil {
		t.Fatal("removed with nothing saying the write made it")
	}
	_, left, err := RestoreExact(ctx, f.run, f.w, s, paths, made)
	if err != nil || !slices.Equal(left, []string{"dir/edited"}) {
		t.Fatalf("left %q, %v", left, err)
	}
	for _, p := range []string{"new", "deep", "dir/made", "dir/.gitignore", "dir/link", "dir/made.log"} {
		if _, err := os.Lstat(filepath.Join(f.r.Dir, p)); err == nil {
			t.Errorf("%s left", p)
		}
	}
	for p, want := range map[string]string{"dir/a": "a", "dir/keep.log": "ignored, there before", "dir/untracked": "there before",
		"dir/secret.env": "the user's, ignored by the write's rule", "dir/mine": "the user's", "dir/edited": "made, then edited"} {
		if got := read(t, filepath.Join(f.r.Dir, p)); got != want {
			t.Errorf("%s: %q", p, got)
		}
	}

	// With ignored files kept, an ignored file the snapshot did not have
	// was not there.
	s = f.take(Options{Reason: "x", Paths: []string{"dir"}, Ignored: true})
	f.r.Write("dir/later.log", "made")
	if made, err = FindMade(ctx, f.run, f.w, s, []string{"dir"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := RestoreExact(ctx, f.run, f.w, s, []string{"dir"}, made); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(f.r.Dir, "dir/later.log")); err == nil {
		t.Error("an ignored file made since was left")
	}
	if read(t, filepath.Join(f.r.Dir, "dir/keep.log")) != "ignored, there before" {
		t.Error("an ignored file kept was lost")
	}
}

// What a write made is told from what was there by the snapshot and the
// ignored files it listed: a file there before, ignored by a rule the
// write took away, is not the write's; one the write made at a path
// where nothing was, in an ignored folder, or in a folder it made and
// ignores whole, is; an ignored file in a folder ignored whole before is
// not the write's, and one not ignored there cannot be told, and is said
// to be.
func TestFindMadeIgnored(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.r.Write(".gitignore", "build/\ngen/\n*.key\n")
	f.r.Write("cfg/.gitignore", "local.env\n")
	f.r.Write("cfg/x", "x")
	f.r.Commit("one")
	f.r.Write("cfg/local.env", "SECRET=1")
	f.r.Write("lib/build/old", "built")
	f.r.Write("lib/y", "y")
	f.r.Write("gen/old", "generated")
	f.r.Write("keys/a.key", "key")
	paths := []string{"cfg", "lib", "gen/new", "out", "keys"}
	s := f.take(Options{Reason: "x", Paths: paths})
	f.r.Git("rm", "-q", "cfg/.gitignore")
	f.r.Write("lib/build/new", "made")
	f.r.Write("gen/new", "made")
	f.r.Write("out/.gitignore", "*\n")
	f.r.Write("out/a", "made")
	f.r.Write("keys/b.key", "made or not")
	f.r.Write("keys/note", "made or not")
	made, err := FindMade(ctx, f.run, f.w, s, paths)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range made.Files {
		got = append(got, m.Path)
	}
	if want := []string{"gen/new", "out/.gitignore", "out/a"}; !slices.Equal(got, want) {
		t.Fatalf("made %q, want %q", got, want)
	}
	if !slices.Equal(made.untold, []string{"keys/note"}) {
		t.Fatalf("untold %q", made.untold)
	}
}

// A file changed in the same tick as the index was written is racy: git
// reads it again instead of trusting its stat data, and so must the copy
// of the index a snapshot works in. Without ctime and inode, as on
// Windows, only the times tell.
func TestRacyFile(t *testing.T) {
	f := setup(t)
	f.r.Git("config", "core.trustctime", "false")
	f.r.Git("config", "core.checkStat", "minimal")
	f.r.Write("a", "A")
	f.r.Commit("one")
	at := time.Now().Add(-time.Hour).Truncate(time.Second)
	a, index := filepath.Join(f.r.Dir, "a"), filepath.Join(f.w.GitDir, "index")
	os.Chtimes(a, at, at)
	f.r.Git("add", "a")
	os.Chtimes(index, at, at)
	f.r.Write("a", "B")
	os.Chtimes(a, at, at)
	// diff-files leaves the index as it is; status would rewrite it and
	// mark the entry for reading again.
	if d := f.r.Git("diff-files", "--name-only"); d != "a\n" {
		t.Fatalf("git sees %q", d)
	}
	s := f.take(Options{Reason: "Discard", Paths: []string{"a"}})
	if got := f.r.Git("show", s.Commit+":a"); got != "B" {
		t.Fatalf("snapshot kept %q", got)
	}
}

// A repository whose refs go to git, which refuses it as well, has no
// snapshots to list, and listing them does not fail.
func TestListWithoutRefReader(t *testing.T) {
	if list, err := List(context.Background(), nil, nil); list != nil || err != nil {
		t.Fatalf("%v %v", list, err)
	}
}

// A sparse checkout with files outside its cone, untracked or ignored,
// is kept all the same: git add refuses them without --sparse.
func TestSparseCheckout(t *testing.T) {
	f := setup(t)
	if g := f.run.Git(); g == nil || !g.Version.Has(gitexe.AddSparse) {
		t.Skip("git before add --sparse")
	}
	f.r.Write(".gitignore", ".idea/workspace.xml\n")
	f.r.Write("src/a", "a")
	f.r.Write("docs/b", "b")
	f.r.Commit("one")
	f.r.Git("sparse-checkout", "set", "--cone", "src")
	f.r.Write("other/new", "n")
	f.r.Write(".idea/workspace.xml", "x")
	f.take(Options{Reason: "Snapshot now"})
	s := f.take(Options{Reason: "Snapshot now", Ignored: true})
	if out := f.r.Git("show", s.Commit+":.idea/workspace.xml"); out != "x" {
		t.Fatalf("the ignored file: %q", out)
	}
}

// A repository inside with no commit, one just made with git init, is
// left out of a snapshot, which keeps the rest rather than failing.
func TestRepositoryWithoutCommitLeftOut(t *testing.T) {
	f := setup(t)
	f.r.Write("a", "1")
	f.r.Commit("one")
	f.r.Git("init", "-q", "d/inner")
	f.r.Write("d/new", "n")
	f.r.Write("top", "t")
	all := f.take(Options{Reason: "Snapshot now"})
	some := f.take(Options{Reason: "Delete d", Paths: []string{"d"}})
	for _, c := range []struct {
		s    Snapshot
		path string
	}{{all, "top"}, {all, "d/new"}, {some, "d/new"}} {
		if out := f.r.Git("show", c.s.Commit+":"+c.path); out == "" {
			t.Fatalf("%s not kept", c.path)
		}
	}
}

// A worktree's snapshot leaves its ignored folders out, one whose name
// starts with a space too, and keeps its ignored files.
func TestWorktreeLeavesIgnoredFoldersOut(t *testing.T) {
	f := setup(t)
	f.r.Write(".gitignore", " out/\n.env\n")
	f.r.Write("a", "1")
	f.r.Commit("one")
	wt, _ := filepath.EvalSymlinks(t.TempDir())
	wt = filepath.Join(wt, "wt")
	f.r.Git("worktree", "add", "-q", "--detach", wt)
	for name, body := range map[string]string{" out/x": "x", ".env": "e"} {
		p := filepath.Join(wt, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := f.take(Options{Reason: "Remove worktree", Ignored: true, Worktree: wt})
	if out := f.r.Git("show", s.Commit+":.env"); out != "e" {
		t.Fatalf(".env: %q", out)
	}
	if _, err := f.r.Run(nil, "cat-file", "-e", s.Commit+": out/x"); err == nil {
		t.Fatal("the ignored folder kept")
	}
}
