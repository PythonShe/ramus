package diff

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

func oid(t *testing.T, r *gittest.Repo, rev string) []byte {
	b, err := hex.DecodeString(strings.TrimSpace(r.Git("rev-parse", rev)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func format(r *Raw) string {
	var b strings.Builder
	for i := 0; i < r.Len(); i++ {
		om, oo := r.Old(i)
		nm, no := r.New(i)
		st := string(r.Status(i))
		if r.Status(i) == 'R' || r.Status(i) == 'C' {
			st += fmt.Sprintf("%03d", r.Score(i))
		}
		fmt.Fprintf(&b, ":%06o %06o %x %x %s\x00%s\x00", om, nm, oo, no, st, r.OldPath(i))
		if r.Status(i) == 'R' || r.Status(i) == 'C' {
			fmt.Fprintf(&b, "%s\x00", r.Path(i))
		}
	}
	return b.String()
}

func TestRenames(t *testing.T) {
	r := gittest.Init(t)
	body := strings.Repeat("a line long enough to compare\n", 20)
	for i := 0; i < 4; i++ {
		r.Write(fmt.Sprintf("old/f%d", i), fmt.Sprint(i)+body)
	}
	r.Write("keep", "1")
	root := r.Commit("one")
	for i := 0; i < 4; i++ {
		r.Git("mv", fmt.Sprintf("old/f%d", i), fmt.Sprintf("new f%d", i))
	}
	for i := 1; i < 4; i++ {
		// Inexact renames: only those count against the rename limit.
		r.Write(fmt.Sprintf("new f%d", i), fmt.Sprint(i)+body+"changed\n")
	}
	r.Write("keep", "2")
	r.Git("add", "-A")
	r.Commit("two")

	run := testrun.Runner(t)
	raw, lim, err := Renames(context.Background(), run, r.GitDir(), 20, oid(t, r, "HEAD~"), oid(t, r, "HEAD"))
	if err != nil || lim.Skipped {
		t.Fatal(err, lim)
	}
	want := r.Git("diff-tree", "-r", "-z", "--raw", "--no-abbrev", "-M", "HEAD~", "HEAD")
	if got := format(raw); got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	renamed := 0
	for i := 0; i < raw.Len(); i++ {
		if raw.Status(i) == 'R' {
			renamed++
		}
	}
	if renamed != 4 {
		t.Errorf("%d renames", renamed)
	}

	raw, _, err = Renames(context.Background(), run, r.GitDir(), 20, nil, oid(t, r, root))
	if err != nil || raw.Len() != 5 || raw.Status(0) != 'A' {
		t.Fatalf("root: %v %d", err, raw.Len())
	}

	// Past the rename limit, git lists deletions and additions and says so.
	r.Git("config", "diff.renameLimit", "1")
	raw, lim, err = Renames(context.Background(), run, r.GitDir(), 20, oid(t, r, "HEAD~"), oid(t, r, "HEAD"))
	if err != nil || !lim.Skipped || lim.Needed < 2 {
		t.Fatalf("limit: %v %+v", err, lim)
	}
	if raw.Status(0) == 'R' && raw.Status(1) == 'R' && raw.Status(2) == 'R' {
		t.Error("renames found past the limit")
	}
}

// A commit compared with the work tree lists what git diff-index lists,
// renames staged and changes not, and counts and patches the same files.
func TestWorkTreeAgainstCommit(t *testing.T) {
	r := gittest.Init(t)
	body := strings.Repeat("a line long enough to compare\n", 20)
	r.Write("moved", body)
	r.Write("a", "1\n")
	r.Commit("one")
	r.Write("a", "1\n2\n")
	r.Commit("two")
	r.Git("mv", "moved", "here")
	r.Write("a", "1\n2\n3\n")
	r.Write("untracked", "u\n")

	ctx := context.Background()
	run := testrun.Runner(t)
	base := oid(t, r, "HEAD~")
	raw, _, err := WorkTreeRenames(ctx, run, r.GitDir(), r.Dir, 20, base)
	if err != nil {
		t.Fatal(err)
	}
	want := r.Git("diff-index", "-z", "--raw", "--no-abbrev", "-M", "HEAD~")
	if got := format(raw); got != want || raw.Len() != 2 {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	ns, err := WorkTreeBaseLines(ctx, run, r.GitDir(), r.Dir, base)
	if err != nil {
		t.Fatal(err)
	}
	if l := ns.Find([]byte("a"), []byte("a")); l != (Lines{Added: 2, Known: true}) {
		t.Errorf("a %+v", l)
	}
	if l := ns.Find([]byte("moved"), []byte("here")); l != (Lines{Known: true}) {
		t.Errorf("rename %+v", l)
	}
	p, err := WorkTreePatch(ctx, run, r.GitDir(), r.Dir, PatchRequest{Source: Head, Base: hex.EncodeToString(base), Paths: []string{"a"}, Context: -1})
	if err != nil || !strings.Contains(string(p.Text), "+2\n+3\n") {
		t.Fatalf("%v %q", err, p.Text)
	}
}

func FuzzParseRaw(f *testing.F) {
	f.Add([]byte(":100644 100644 " + strings.Repeat("1", 40) + " " + strings.Repeat("2", 40) + " R087\x00a\x00b\x00"))
	f.Add([]byte(strings.Repeat("3", 40) + "\x00:000000 100644 " + strings.Repeat("0", 40) + " " + strings.Repeat("2", 40) + " A\x00x\x00"))
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := ParseRaw(data, 20)
		if err != nil {
			return
		}
		for i := 0; i < r.Len(); i++ {
			_, o := r.Old(i)
			_, n := r.New(i)
			if len(o) != 20 || len(n) != 20 || r.Score(i) > 100 {
				t.Fatal("bad entry")
			}
			_, _ = r.Path(i), r.OldPath(i)
		}
	})
}
