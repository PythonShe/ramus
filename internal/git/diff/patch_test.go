package diff

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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

// A large patch is read and parsed into arrays sized once: grown by
// append, the copies came to about five times what is kept.
func TestLargePatchAllocatesAboutWhatItKeeps(t *testing.T) {
	var b strings.Builder
	b.WriteString("diff --git a/f b/f\n--- a/f\n+++ b/f\n")
	for i := range 100_000 {
		fmt.Fprintf(&b, "@@ -%d,3 +%d,3 @@\n a\n-b\n+c\n d\n", i*10+1, i*10+1)
	}
	text := []byte(b.String())
	allocated := func(f func()) uint64 {
		var m0, m1 runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&m0)
		f()
		runtime.ReadMemStats(&m1)
		return m1.TotalAlloc - m0.TotalAlloc
	}
	var p *Parsed
	var err error
	got := allocated(func() { p, err = Parse(text) })
	if err != nil || len(p.Hunks) != 100_000 {
		t.Fatal(err)
	}
	if kept := uint64(p.Size()); got > kept*3/2 {
		t.Errorf("Parse allocated %d bytes for %d kept", got, kept)
	}
	chunk := make([]byte, 32<<10)
	const n = 16 << 20
	var read []byte
	got = allocated(func() {
		c := &capped{max: 64 << 20, cancel: func() {}}
		for w := 0; w < n; w += len(chunk) {
			c.Write(chunk)
		}
		read = c.text()
	})
	if len(read) != n || got > n*5/2 {
		t.Errorf("reading %d bytes allocated %d", len(read), got)
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
