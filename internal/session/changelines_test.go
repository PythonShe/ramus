package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/diff"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

// diffRuns counts the line counts git ran: in the index, in the work tree.
func diffRuns(logged []string) (index, tree int) {
	for _, l := range logged {
		switch {
		case strings.Contains(l, " diff-index "):
			index++
		case strings.Contains(l, " diff-files "):
			tree++
		}
	}
	return index, tree
}

// A count asked for while one runs waits for it, the newest only: the
// one running ends and is handed over, not cancelled.
func TestCountsCoalesce(t *testing.T) {
	runner, logged := scriptRunner(t, `case "$*" in *diff-files*) sleep 0.3;; esac`)
	r := gittest.Init(t)
	r.Write("a", "1\n")
	r.Commit("one")
	r.Write("a", "1\n2\n")
	// Written lately whenever its lines are counted, however long the
	// repository takes to open: no count of it is kept.
	late := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(r.Dir, "a"), late, late); err != nil {
		t.Fatal(err)
	}
	m := make(mainThread, 64)
	s := Open(runner, trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "status", func() bool { return s.State.Status != nil && s.State.Status.Len() == 1 })
	m.settle(t, s)
	st := s.State.Status
	_, before := diffRuns(logged())
	var got []int
	for n := range 3 {
		s.CountChanges(st, nil, func(l ChangeLines) {
			if l.Unstaged[0] != (diff.Lines{Added: 1, Known: true}) {
				t.Errorf("count %d: %+v", n, l.Unstaged[0])
			}
			got = append(got, n)
		})
	}
	m.until(t, "the last count", func() bool { return len(got) == 2 })
	m.pump(100 * time.Millisecond)
	if len(got) != 2 || got[0] != 0 || got[1] != 2 {
		t.Fatalf("handed over %v, want the first and the last", got)
	}
	// Both ran: a was written too lately for its count to be kept.
	if _, after := diffRuns(logged()); after-before != 2 {
		t.Fatalf("%d counts ran", after-before)
	}
}

// A count of files unchanged since the last takes their counts as kept,
// and counts again what changed.
func TestCountsKept(t *testing.T) {
	runner, logged := loggedRunner(t)
	r := gittest.Init(t)
	r.Write("a", "1\n")
	r.Write("b", "1\n")
	r.Commit("one")
	r.Write("a", "1\n2\n")
	r.Write("b", "1\n2\n3\n")
	r.Git("add", "b")
	r.Write("c", "1\n2\n3\n4\n")
	old := time.Now().Add(-time.Hour)
	age := func() {
		for _, f := range []string{"a", "b", "c"} {
			if err := os.Chtimes(filepath.Join(r.Dir, f), old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	age()
	m := make(mainThread, 64)
	s := Open(runner, trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "status", func() bool { return s.State.Status != nil && s.State.Status.Len() == 3 })
	m.settle(t, s)
	count := func() ChangeLines {
		t.Helper()
		var out *ChangeLines
		s.CountChanges(s.State.Status, nil, func(l ChangeLines) { out = &l })
		m.until(t, "the count", func() bool { return out != nil })
		return *out
	}
	check := func(l ChangeLines, a, b, c uint32) {
		t.Helper()
		st := l.For
		for i := range st.Len() {
			var got diff.Lines
			want := map[string]uint32{"a": a, "b": b, "c": c}[string(st.Path(i))]
			if string(st.Path(i)) == "b" {
				got = l.Staged[i]
			} else {
				got = l.Unstaged[i]
			}
			if got.Added != want || !got.Known {
				t.Fatalf("%s: %+v, want %d added", st.Path(i), got, want)
			}
		}
	}
	index, tree := diffRuns(logged())
	check(count(), 1, 2, 4)
	if i, w := diffRuns(logged()); i != index+1 || w != tree+1 {
		t.Fatalf("the first count ran %d and %d", i-index, w-tree)
	}
	index, tree = diffRuns(logged())
	check(count(), 1, 2, 4)
	if i, w := diffRuns(logged()); i != index || w != tree {
		t.Fatalf("a count of what was counted ran %d and %d", i-index, w-tree)
	}
	r.Write("a", "1\n2\n3\n4\n5\n")
	r.Write("c", "1\n")
	age()
	s.Reload()
	m.settle(t, s)
	check(count(), 4, 2, 1)
	if i, w := diffRuns(logged()); i != index || w != tree+1 {
		t.Fatalf("a count after an edit ran %d and %d", i-index, w-tree)
	}

	// The index written after the status was read: git counts the new
	// index, which is not kept under the objects the status names.
	r.Write("d", "1\n")
	r.Git("add", "d")
	s.Reload()
	m.settle(t, s)
	st := s.State.Status
	r.Write("d", "1\n2\n")
	r.Git("add", "d")
	index, tree = diffRuns(logged())
	var out *ChangeLines
	s.CountChanges(st, nil, func(l ChangeLines) { out = &l })
	m.until(t, "the count", func() bool { return out != nil })
	out = nil
	s.CountChanges(st, nil, func(l ChangeLines) { out = &l })
	m.until(t, "the count again", func() bool { return out != nil })
	if i, _ := diffRuns(logged()); i != index+2 {
		t.Fatalf("counts of another index kept: diff-index ran %d times", i-index)
	}
}

// A count that ends after another took the slot, one of a repository
// since closed, leaves that one running: else a third would start beside
// it, sharing the counts kept.
func TestStaleCountLeavesTheSlot(t *testing.T) {
	runner, _ := scriptRunner(t, `case "$*" in *diff-files*) sleep 1;; esac`)
	r := gittest.Init(t)
	r.Write("a", "1\n")
	r.Commit("one")
	r.Write("a", "1\n2\n")
	m := make(mainThread, 64)
	s := Open(runner, trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "status", func() bool { return s.State.Status != nil && s.State.Status.Len() == 1 })
	m.settle(t, s)
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	stale := &countAsk{r: s.Repo, ctx: gone, st: s.State.Status, done: func(ChangeLines) {}}
	ended := false
	cur := &countAsk{r: s.Repo, ctx: context.Background(), st: s.State.Status, done: func(ChangeLines) { ended = true }}
	s.startCount(stale)
	s.startCount(cur)
	m.pump(300 * time.Millisecond)
	if !s.count.running || s.count.cur != cur {
		t.Fatalf("running %v, the current count %v", s.count.running, s.count.cur == cur)
	}
	m.until(t, "the count", func() bool { return ended })
	if s.count.running {
		t.Fatal("still running")
	}
}

// The files of the untracked folders asked for are listed and counted,
// a folder's count adding theirs up; the others are not listed.
func TestCountsFolders(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a", "1\n")
	r.Commit("one")
	r.Write("new/x.txt", "1\n2\n")
	r.Write("new/deep/y.txt", "1\n2\n3\n")
	r.Write("new/pic.bin", "\x00\x01")
	r.Write("other/z.txt", "1\n")
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "status", func() bool { return s.State.Status != nil && s.State.Status.Len() == 2 })
	m.settle(t, s)
	st := s.State.Status
	var out *ChangeLines
	s.CountChanges(st, []string{"new/"}, func(l ChangeLines) { out = &l })
	m.until(t, "the count", func() bool { return out != nil })
	f, ok := out.Folders["new/"]
	if !ok || len(out.Folders) != 1 {
		t.Fatalf("folders listed: %v", out.Folders)
	}
	want := map[string]uint32{"new/deep/y.txt": 3, "new/pic.bin": 0, "new/x.txt": 2}
	if len(f.Paths) != len(want) || f.More != 0 {
		t.Fatalf("files %q, %d more", f.Paths, f.More)
	}
	for k, p := range f.Paths {
		if l := f.Lines[k]; l.Added != want[p] || !l.Known || l.Binary != (p == "new/pic.bin") {
			t.Fatalf("%s: %+v", p, l)
		}
	}
	for i := range st.Len() {
		l := out.Unstaged[i]
		if string(st.Path(i)) == "new/" && (l.Added != 5 || !l.Known) {
			t.Fatalf("the folder's count: %+v", l)
		}
		if string(st.Path(i)) == "other/" && l.Known {
			t.Fatalf("a folder not asked for counted: %+v", l)
		}
	}
}
