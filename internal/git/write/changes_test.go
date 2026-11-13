package write

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ramus/internal/git/diff"
	"ramus/internal/git/gittest"
	"ramus/internal/git/refs"
)

func TestChanges(t *testing.T) {
	r, q, _ := newQueue(t)
	q.Refs = refs.New(r.GitDir(), r.GitDir(), 20)
	w := Where{GitDir: r.GitDir(), WorkTree: r.Dir}
	r.Write("a", "1\n2\n3\n")
	r.Write("b", "b\n")
	r.Commit("one")
	r.Write("a", "1\nTWO\n3\nfour\n")
	r.Write("new file", "n\n")
	r.Write("untracked dir/x", "x\n")
	os.Remove(filepath.Join(r.Dir, "b"))
	ok := func(j *Job) {
		t.Helper()
		if _, err := wait(t, j); err != nil {
			t.Fatalf("%s: %v", j.Op.Name, err)
		}
	}
	status := func() string { return r.Git("status", "--porcelain") }
	read := func(name string) string { b, _ := os.ReadFile(filepath.Join(r.Dir, name)); return string(b) }
	patch := func() *diff.Parsed {
		t.Helper()
		pt, err := diff.WorkTreePatch(t.Context(), q.r, r.GitDir(), r.Dir, diff.PatchRequest{Source: diff.Unstaged, Paths: []string{"a"}, Context: -1, ForApply: true})
		if err != nil {
			t.Fatal(err)
		}
		p, err := diff.Parse(pt.Text)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}

	ok(q.Submit(Stage(w, []string{"b", "new file"})))
	if s := status(); !strings.Contains(s, "D  b") || !strings.Contains(s, `A  "new file"`) {
		t.Fatalf("after stage:\n%s", s)
	}
	ok(q.Submit(Unstage(w, []string{"b", "new file"}, false)))
	if s := status(); !strings.Contains(s, " D b") || !strings.Contains(s, `?? "new file"`) {
		t.Fatalf("after unstage:\n%s", s)
	}

	// Stage the 2 -> TWO change of a, not the added "four".
	p := patch()
	sel, err := p.Select(0, false, func(i int) bool {
		s := string(p.Bytes(p.Lines[i].Text))
		return s == "2" || s == "TWO"
	})
	if err != nil {
		t.Fatal(err)
	}
	ok(q.Submit(Apply(w, Partial{Path: "a", Patch: sel, Cached: true})))
	if got := r.Git("show", ":a"); got != "1\nTWO\n3\n" {
		t.Fatalf("index a %q", got)
	}

	// Discard what is left ("four") from the work tree, undo, redo.
	p = patch()
	sel, err = p.Select(0, true, func(int) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	ok(q.Submit(Apply(w, Partial{Path: "a", Patch: sel, Reverse: true})))
	if got := read("a"); got != "1\nTWO\n3\n" {
		t.Fatalf("work tree a %q", got)
	}
	if undo, _ := q.History.Next(); undo != "Discard lines of a" {
		t.Fatalf("next undo %q", undo)
	}
	ok(q.Undo())
	if got := read("a"); got != "1\nTWO\n3\nfour\n" {
		t.Fatalf("after undo a %q", got)
	}
	ok(q.Redo())
	if got := read("a"); got != "1\nTWO\n3\n" {
		t.Fatalf("after redo a %q", got)
	}

	// Discard the deletion of b, delete the untracked files, undo that.
	ok(q.Submit(Discard(w, []string{"b"})))
	if read("b") != "b\n" {
		t.Fatal("b not back")
	}
	del, err := Delete(w, []string{"new file", "untracked dir/"})
	if err != nil {
		t.Fatal(err)
	}
	ok(q.Submit(del))
	if _, err := os.Stat(filepath.Join(r.Dir, "untracked dir")); err == nil || read("new file") != "" {
		t.Fatal("untracked files left")
	}
	ok(q.Undo())
	if read("new file") != "n\n" || read("untracked dir/x") != "x\n" {
		t.Fatalf("delete not undone:\n%s", status())
	}

	// Unstage in a repository with no commit yet.
	fresh := gittest.Init(t)
	fresh.Write("f", "x")
	fresh.Git("add", "f")
	ok(q.Submit(Unstage(Where{GitDir: fresh.GitDir(), WorkTree: fresh.Dir}, []string{"f"}, true)))
	if s := fresh.Git("status", "--porcelain"); s != "?? f\n" {
		t.Fatalf("unborn unstage:\n%s", s)
	}
}

func TestIgnore(t *testing.T) {
	r, q, _ := newQueue(t)
	w := Where{GitDir: r.GitDir(), WorkTree: r.Dir}
	r.Write(".gitignore", "*.log")
	names := []string{"build out", "#hash", "!bang", "star*file", "trailing ", "a[1]", `back\slash`, "dir/sub"}
	for _, n := range names {
		r.Write(n, "x")
		op, err := Ignore(w, IgnorePattern(n))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := wait(t, q.Submit(op)); err != nil {
			t.Fatal(err)
		}
	}
	r.Write("starXfile", "x")
	r.Write("other/dir/sub", "x")
	for _, n := range append(names, "x.log") {
		if out, err := r.Run(nil, "check-ignore", "-q", "--no-index", "--", n); err != nil {
			t.Errorf("%q not ignored: %v %s", n, err, out)
		}
	}
	for _, n := range []string{"starXfile", "other/dir/sub"} {
		if _, err := r.Run(nil, "check-ignore", "-q", "--no-index", "--", n); err == nil {
			t.Errorf("%q ignored too", n)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(r.Dir, ".gitignore")); !strings.HasPrefix(string(b), "*.log\n/build out\n") {
		t.Fatalf(".gitignore\n%s", b)
	}
	if _, err := Ignore(w, "a\nb"); err == nil {
		t.Fatal("pattern with a line break")
	}
}
