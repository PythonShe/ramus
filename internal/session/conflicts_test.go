package session

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"ramus/internal/git/conflict"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/write"
)

func conflictRepo(t *testing.T) *gittest.Repo {
	r := gittest.Init(t)
	r.Write("a.txt", "one\n")
	r.Write("b.bin", "\x00base")
	r.Write("c.txt", "c\n")
	r.Write("d.txt", "d\n")
	r.Commit("base")
	r.Git("switch", "-q", "-c", "topic")
	r.Write("a.txt", "theirs\n")
	r.Write("b.bin", "\x00theirs")
	r.Git("rm", "-q", "c.txt")
	r.Write("d.txt", "d\nmore\n")
	r.Commit("topic")
	r.Git("switch", "-q", "main")
	r.Write("a.txt", "ours\n")
	r.Write("b.bin", "\x00ours")
	r.Write("c.txt", "c changed\n")
	r.Write("d.txt", "first\nd\n")
	r.Commit("main")
	return r
}

func TestPreviewMergeAndLoadConflict(t *testing.T) {
	r := conflictRepo(t)
	s, m := openFor(t, r)
	preview := func(what string) MergePreview {
		var got *MergePreview
		s.PreviewMerge(what, func(p MergePreview, err error) {
			if err != nil {
				t.Error(err)
			}
			got = &p
		})
		m.until(t, "the preview", func() bool { return got != nil })
		return *got
	}
	before := r.Git("count-objects")
	p := preview("topic")
	if !slices.Equal(p.BothChanged, []string{"a.txt", "b.bin", "c.txt", "d.txt"}) || !slices.Equal(p.Conflicts, []string{"a.txt", "b.bin", "c.txt"}) {
		t.Fatalf("preview %+v", p)
	}
	if st := r.Git("status", "--porcelain"); st != "" {
		t.Fatalf("the preview touched the work tree: %q", st)
	}
	// Nor the objects: a preview is a read, and one in a repository not
	// trusted must write nothing to it.
	if objects := r.Git("count-objects"); objects != before {
		t.Fatalf("the preview wrote objects: %q, before %q", objects, before)
	}
	if p := preview("main~1"); !p.UpToDate {
		t.Fatalf("up to date %+v", p)
	}

	r.Run(nil, "merge", "topic")
	load := func(path string) ConflictFile {
		var got *ConflictFile
		s.LoadConflict(path, func(cf ConflictFile) { got = &cf })
		m.until(t, "the file", func() bool { return got != nil })
		return *got
	}
	a := load("a.txt")
	if a.Err != nil || conflict.Count(a.Segments) != 1 || !a.Ours || !a.Theirs || a.Seen.Path != "a.txt" {
		t.Fatalf("a.txt %+v", a)
	}
	if b := load("b.bin"); !b.Binary || b.Segments != nil {
		t.Fatalf("b.bin %+v", b)
	}
	if c := load("c.txt"); !c.Ours || c.Theirs {
		t.Fatalf("c.txt %+v", c)
	}
}

// A conflicted file read for the merge editor carries the sum of its
// bytes, so a save over it is refused when an edit elsewhere kept its
// size and time.
func TestLoadConflictRefusesASaveOverAnEditOfTheSameSize(t *testing.T) {
	r := conflictRepo(t)
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "the status", func() bool { return s.State.Status != nil })
	r.Run(nil, "merge", "topic")
	var cf *ConflictFile
	s.LoadConflict("a.txt", func(got ConflictFile) { cf = &got })
	m.until(t, "the file", func() bool { return cf != nil })
	if cf.Err != nil {
		t.Fatal(cf.Err)
	}
	p := filepath.Join(r.Dir, "a.txt")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	edited := []byte(string(data[:len(data)-2]) + "X\n")
	if err := os.WriteFile(p, edited, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, cf.Seen.ModTime, cf.Seen.ModTime); err != nil {
		t.Fatal(err)
	}
	op, err := write.Resolve(s.Where(), "a.txt", []byte("resolved\n"), cf.Seen)
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.Submit(op)
	if err != nil {
		t.Fatal(err)
	}
	m.until(t, "the save", func() bool {
		select {
		case <-j.Done():
			return true
		default:
			return false
		}
	})
	var se *write.StaleError
	if _, err := j.Result(); !errors.As(err, &se) {
		t.Fatalf("saved over the edit: %v", err)
	}
	if b, _ := os.ReadFile(p); string(b) != string(edited) {
		t.Fatalf("a.txt %q", b)
	}
}
