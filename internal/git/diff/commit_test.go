package diff

import (
	"context"
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
		old, new, err := BlobPair(cf, o, n)
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
	if _, _, err := BlobPair(cf, a, nil); err == nil {
		t.Error("a commit read as a blob")
	}
}
