package ui

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"ramus/internal/git/diff"
	"ramus/internal/git/status"
	"ramus/internal/session"
)

const zeroOID = "0000000000000000000000000000000000000000"

// fakeStatus makes porcelain v2 output: tracked entries with their XY,
// then untracked paths.
func fakeStatus(t testing.TB, tracked map[string]string, untracked ...string) *status.Status {
	t.Helper()
	var b strings.Builder
	var paths []string
	for p := range tracked {
		paths = append(paths, p)
	}
	sortStrings(paths)
	for _, p := range paths {
		fmt.Fprintf(&b, "1 %s N... 100644 100644 100644 %s %s %s\x00", tracked[p], zeroOID, zeroOID, p)
	}
	sortStrings(untracked)
	for _, p := range untracked {
		fmt.Fprintf(&b, "? %s\x00", p)
	}
	st, err := status.Parse([]byte(b.String()), 20)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func sortStrings(s []string) { sort.Strings(s) }

func TestBuildChanges(t *testing.T) {
	st := fakeStatus(t, map[string]string{"b/one": "M.", "b/two": "MM", "c": ".D"}, "a", "b/new", "dir/")
	m := buildChanges(st, "", false, nil, nil)
	got := func(sec section) []string {
		var out []string
		for _, i := range m.files[sec] {
			out = append(out, string(st.Path(int(i))))
		}
		return out
	}
	if s := got(secStaged); strings.Join(s, ",") != "b/one,b/two" {
		t.Fatalf("staged %q", s)
	}
	// Untracked entries merge into path order with tracked ones.
	if s := got(secUnstaged); strings.Join(s, ",") != "a,b/new,b/two,c,dir/" {
		t.Fatalf("unstaged %q", s)
	}
	if n := len(m.under(secUnstaged, "b", true)); n != 2 {
		t.Fatalf("under b: %d", n)
	}

	tree := buildChanges(st, "", true, map[openKey]bool{{secStaged, "b"}: true}, nil)
	var names []string
	for _, r := range tree.rows {
		names = append(names, fmt.Sprint(r.depth)+tree.name(r))
	}
	want := "0Staged 0b 0Unstaged 0b 1new 1two 0a 0c 0dir/"
	if s := strings.Join(names, " "); s != want {
		t.Fatalf("tree rows\n%s\nwant\n%s", s, want)
	}

	f := buildChanges(st, "TW", false, nil, nil)
	if len(f.files[secStaged]) != 1 || len(f.files[secUnstaged]) != 1 {
		t.Fatalf("filter kept %d, %d", len(f.files[secStaged]), len(f.files[secUnstaged]))
	}
}

// A filter of a letter past ASCII keeps the paths that hold it as typed:
// the filter folds case as the paths do, in ASCII alone.
func TestFilterFoldsAsPathsDo(t *testing.T) {
	st := fakeStatus(t, nil, "Äpfel.txt", "äpfel.txt", "APFEL.txt")
	shown := func(filter string) []string {
		var out []string
		for _, i := range buildChanges(st, filter, false, nil, nil).files[secUnstaged] {
			out = append(out, string(st.Path(int(i))))
		}
		return out
	}
	if got := shown("Äpfel"); len(got) != 1 || got[0] != "Äpfel.txt" {
		t.Fatalf("filter Äpfel kept %q", got)
	}
	if got := shown("apfel"); len(got) != 1 || got[0] != "APFEL.txt" {
		t.Fatalf("filter apfel kept %q", got)
	}
}

// BenchmarkBuildChanges builds the list for 390,000 changed files, the
// size of the monorepo fixture, flat and as a tree.
func BenchmarkBuildChanges(b *testing.B) {
	tracked := map[string]string{}
	for i := range 390_000 {
		tracked[fmt.Sprintf("pkg%03d/mod%03d/file%05d.go", i%400, i%997, i)] = ".M"
	}
	st := fakeStatus(b, tracked)
	for _, tree := range []bool{false, true} {
		b.Run(fmt.Sprint("tree=", tree), func(b *testing.B) {
			for b.Loop() {
				buildChanges(st, "", tree, nil, nil)
			}
		})
	}
	b.Run("filter", func(b *testing.B) {
		for b.Loop() {
			buildChanges(st, "mod123", false, nil, nil)
		}
	})
}

func TestEntryNote(t *testing.T) {
	line := func(xy, sub, mh, mi, mw, path string) string {
		return fmt.Sprintf("1 %s %s %s %s %s %s %s %s\x00", xy, sub, mh, mi, mw, zeroOID, zeroOID, path)
	}
	out := line(".M", "N...", "100644", "100644", "100755", "run.sh") +
		line("M.", "N...", "100644", "100755", "100755", "staged.sh") +
		line(".T", "N...", "100644", "100644", "120000", "link") +
		line(".M", "SC.U", "160000", "160000", "160000", "vendor/lib")
	st, err := status.Parse([]byte(out), 20)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []struct {
		sec  section
		note string
	}{
		{secUnstaged, "file → executable"},
		{secStaged, "file → executable"},
		{secUnstaged, "now a symlink"},
		{secUnstaged, "submodule: new commits, untracked content"},
	} {
		if got := entryNote(st, i, want.sec); got != want.note {
			t.Errorf("%s: %q, want %q", st.Path(i), got, want.note)
		}
	}
}

// A folder of the tree shows the sum of its files' counts, nested ones
// with them, and no more: not a neighbour whose name it starts.
func TestFolderLines(t *testing.T) {
	st := fakeStatus(t, map[string]string{"b/one": ".M", "b/sub/two": ".M", "b.go": ".M", "bc/three": ".M"})
	cv := newChangesState()
	cv.lines = session.ChangeLines{For: st, Staged: make([]diff.Lines, st.Len()), Unstaged: make([]diff.Lines, st.Len())}
	added := map[string]uint32{}
	for i := range st.Len() {
		cv.lines.Unstaged[i] = diff.Lines{Added: uint32(i + 1), Deleted: 1, Known: true}
		added[string(st.Path(i))] = uint32(i + 1)
	}
	m := buildChanges(st, "", true, nil, nil)
	for dir, want := range map[string]lineSum{
		"b":     {added["b/one"] + added["b/sub/two"], 2},
		"b/sub": {added["b/sub/two"], 1},
		"bc":    {added["bc/three"], 1},
	} {
		if got := cv.folderLines(m, secUnstaged, dir); got != want {
			t.Errorf("%s: %+v, want %+v", dir, got, want)
		}
	}
}

func TestCoAuthorList(t *testing.T) {
	got := coAuthorList("Ada Lovelace <ada@example.com>, Alan Turing <alan@example.com>\nGrace Hopper <grace@example.com>; junk")
	want := []string{"Ada Lovelace <ada@example.com>", "Alan Turing <alan@example.com>", "Grace Hopper <grace@example.com>"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("%q", got)
	}
}
