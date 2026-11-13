package session

import (
	"slices"
	"testing"

	"ramus/internal/git/conflict"
	"ramus/internal/git/gittest"
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
	p := preview("topic")
	if !slices.Equal(p.BothChanged, []string{"a.txt", "b.bin", "c.txt", "d.txt"}) || !slices.Equal(p.Conflicts, []string{"a.txt", "b.bin", "c.txt"}) {
		t.Fatalf("preview %+v", p)
	}
	if st := r.Git("status", "--porcelain"); st != "" {
		t.Fatalf("the preview touched the work tree: %q", st)
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
