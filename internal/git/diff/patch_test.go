package diff

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

func TestWorkTreePatch(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a.txt", "one\ntwo\n")
	r.Write("upper.low", "hello\n")
	r.Write(".gitattributes", "*.low diff=upper\n")
	r.Write("日本 語.txt", "x\n")
	r.Commit("one")
	r.Git("config", "diff.upper.textconv", "tr a-z A-Z <")
	r.Write("a.txt", "one\nTWO\n")
	r.Write("upper.low", "hello world\n")
	r.Write("日本 語.txt", "y\n")
	r.Write("staged", "s\n")
	r.Git("add", "staged")
	r.Write("new file", "n1\nn2\n")
	run := testrun.Runner(t)
	get := func(req PatchRequest) string {
		t.Helper()
		p, err := WorkTreePatch(context.Background(), run, r.GitDir(), r.Dir, req)
		if err != nil {
			t.Fatal(err)
		}
		return string(p.Text)
	}
	un := get(PatchRequest{Source: Unstaged, Context: -1})
	for _, want := range []string{"+TWO", "+HELLO WORLD", "diff --git a/日本 語.txt b/日本 語.txt"} {
		if !strings.Contains(un, want) {
			t.Errorf("unstaged patch lacks %q:\n%s", want, un)
		}
	}
	if strings.Contains(un, "staged") {
		t.Error("unstaged patch has the staged file")
	}
	if st := get(PatchRequest{Source: Staged, Context: -1}); !strings.Contains(st, "+s") || strings.Contains(st, "TWO") {
		t.Errorf("staged patch:\n%s", st)
	}
	if one := get(PatchRequest{Source: Unstaged, Paths: []string{"a.txt"}, Context: 0}); strings.Contains(one, "upper") || !strings.Contains(one, "@@ -2 +2 @@") {
		t.Errorf("one path, no context:\n%s", one)
	}
	if lit := get(PatchRequest{Source: Unstaged, Paths: []string{"*.txt"}, Context: -1}); lit != "" {
		t.Errorf("a pattern matched as a pattern:\n%s", lit)
	}
	if ut := get(PatchRequest{Source: Untracked, Paths: []string{"new file"}, Context: -1}); !strings.Contains(ut, "+n2") {
		t.Errorf("untracked:\n%s", ut)
	}
	if _, err := WorkTreePatch(context.Background(), run, r.GitDir(), r.Dir, PatchRequest{Source: Untracked}); err == nil {
		t.Error("untracked without a path")
	}
}

func TestWorkTreePatchTruncated(t *testing.T) {
	r := gittest.Init(t)
	r.Write("big", "")
	r.Commit("one")
	os.WriteFile(filepath.Join(r.Dir, "big"), []byte(strings.Repeat("a line of text\n", 200_000)), 0o644)
	p, err := WorkTreePatch(context.Background(), testrun.Runner(t), r.GitDir(), r.Dir, PatchRequest{Context: -1, MaxBytes: 64 << 10})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Truncated || len(p.Text) > 64<<10 || len(p.Text) < 60<<10 || p.Text[len(p.Text)-1] != '\n' {
		t.Fatalf("truncated %v, %d bytes", p.Truncated, len(p.Text))
	}
}
