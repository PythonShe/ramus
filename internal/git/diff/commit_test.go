package diff

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"ramus/internal/git/catfile"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

func TestCommitPatchAndBlobPair(t *testing.T) {
	r := gittest.Init(t)
	body := strings.Repeat("shared line\n", 30)
	r.Write("old name", "v1\n"+body)
	r.Write("gone", "bye\n")
	r.Commit("one")
	r.Git("mv", "old name", "new name")
	r.Write("new name", "v2\n"+body)
	r.Git("rm", "-q", "gone")
	r.Commit("two")
	run := testrun.Runner(t)
	a, b := oid(t, r, "HEAD~"), oid(t, r, "HEAD")

	p, err := CommitPatch(context.Background(), run, r.GitDir(), a, b, []string{"old name", "new name"}, -1, 0, AllWhitespace)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(p.Text), "rename from old name") || !strings.Contains(string(p.Text), "+v2") {
		t.Fatalf("patch:\n%s", p.Text)
	}

	raw, _, err := Renames(context.Background(), run, r.GitDir(), 20, a, b)
	if err != nil {
		t.Fatal(err)
	}
	cf, err := catfile.Start(context.Background(), run, r.GitDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cf.Close()
	for i := 0; i < raw.Len(); i++ {
		_, o := raw.Old(i)
		_, n := raw.New(i)
		old, new, err := BlobPair(context.Background(), cf, o, n)
		if err != nil {
			t.Fatal(err)
		}
		switch string(raw.Path(i)) {
		case "new name":
			if string(old) != "v1\n"+body || string(new) != "v2\n"+body {
				t.Errorf("rename pair %q %q", old, new)
			}
		case "gone":
			if string(old) != "bye\n" || new != nil {
				t.Errorf("deleted pair %q %q", old, new)
			}
		}
	}
	if _, _, err := BlobPair(context.Background(), cf, a, nil); err == nil {
		t.Error("a commit read as a blob")
	}
	// A read its caller gave up on stops, with the caller's error.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, o := raw.Old(0)
	if _, _, err := BlobPair(cancelled, cf, o, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
}

// A pull request's patch has the hunks its host shows, whatever the
// repository's config says of context, hunks and textconv; git with the
// config as it is would number other lines.
func TestHostPatch(t *testing.T) {
	r := gittest.Init(t)
	var lines []string
	for i := range 40 {
		lines = append(lines, "line "+strconv.Itoa(i))
	}
	r.Write("a.txt", strings.Join(lines, "\n")+"\n")
	r.Write(".gitattributes", "*.txt diff=upper\n")
	r.Commit("one")
	lines[10], lines[20] = "ten", "twenty"
	r.Write("a.txt", strings.Join(lines, "\n")+"\n")
	r.Commit("two")
	r.Git("config", "diff.context", "10")
	r.Git("config", "diff.interHunkContext", "20")
	r.Git("config", "diff.upper.textconv", "tr a-z A-Z <")
	run := testrun.Runner(t)
	a, b := oid(t, r, "HEAD~"), oid(t, r, "HEAD")

	p, err := HostPatch(context.Background(), run, r.GitDir(), a, b, []string{"a.txt"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(p.Text)
	if err != nil || len(parsed.Files) != 1 {
		t.Fatalf("%v: %+v", err, parsed)
	}
	h := parsed.Hunks
	if len(h) != 2 || h[0].NewStart != 8 || h[0].NewCount != 7 || h[1].NewStart != 18 || h[1].NewCount != 7 {
		t.Fatalf("hunks %+v", h)
	}
	if strings.Contains(string(p.Text), "LINE") {
		t.Fatalf("textconv ran:\n%s", p.Text)
	}
	// Without prefixes in the config, a file in a folder named b is still
	// named whole.
	r.Git("config", "diff.noprefix", "true")
	r.Write("b/a.txt", "x\n")
	r.Commit("three")
	p, err = HostPatch(context.Background(), run, r.GitDir(), b, oid(t, r, "HEAD"), []string{"b/a.txt"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if parsed, err = Parse(p.Text); err != nil || len(parsed.Files) != 1 || parsed.Path(0) != "b/a.txt" {
		t.Fatalf("%v:\n%s", err, p.Text)
	}
	r.Git("config", "--unset", "diff.noprefix")

	// The config does change the patch git makes by default.
	if plain := r.Git("diff", "HEAD~2", "HEAD~"); !strings.Contains(plain, "LINE") || strings.Count(plain, "@@ -") != 1 {
		t.Fatalf("config not in effect:\n%s", plain)
	}
}
