package library

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"ramus/internal/issuelink"
)

func names(rs []Repo) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Name)
	}
	return out
}

func TestLibrary(t *testing.T) {
	root := t.TempDir()
	dirs := map[string]string{}
	for _, n := range []string{"ramus", "linux", "ramus-fixtures", "notes"} {
		dirs[n] = filepath.Join(root, "src", n)
		os.MkdirAll(dirs[n], 0o755)
	}
	p := filepath.Join(root, "library.json")
	l, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Unix(1_700_000_000, 0)
	for i, n := range []string{"linux", "ramus", "notes", "ramus-fixtures"} {
		if err := l.Opened(dirs[n], t0.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	l.Opened(dirs["linux"]+"/", t0.Add(10*time.Hour))
	if got := names(l.Recent(3)); !slices.Equal(got, []string{"linux", "ramus-fixtures", "notes"}) {
		t.Fatalf("recent %q", got)
	}
	if got := names(l.Search("RAMUS")); !slices.Equal(got, []string{"ramus-fixtures", "ramus"}) {
		t.Fatalf("search %q", got)
	}
	if got := names(l.Search("src ram fix")); !slices.Equal(got, []string{"ramus-fixtures"}) {
		t.Fatalf("search words %q", got)
	}
	if got := names(l.Search("src")); len(got) != 4 {
		t.Fatalf("path match %q", got)
	}
	l.SetFavourite(dirs["notes"], true)
	l.Rename(dirs["notes"], "  My notes ")
	if got := names(l.Favourites()); !slices.Equal(got, []string{"My notes"}) {
		t.Fatalf("favourites %q", got)
	}
	if err := l.SetGroup("Work", []string{dirs["ramus"], dirs["ramus-fixtures"], dirs["ramus"]}); err != nil {
		t.Fatal(err)
	}
	if err := l.SetGroup("", nil); err == nil {
		t.Fatal("group without a name")
	}

	back, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if g := back.Groups(); len(g) != 1 || len(g[0].Paths) != 2 || g[0].Name != "Work" {
		t.Fatalf("groups %+v", g)
	}
	if got := names(back.Favourites()); !slices.Equal(got, []string{"My notes"}) {
		t.Fatalf("reloaded favourites %q", got)
	}
	back.Forget(dirs["ramus"])
	if g := back.Groups(); len(g[0].Paths) != 1 || len(back.Recent(10)) != 3 {
		t.Fatalf("forget %+v", g)
	}
	back.SetGroup("Work", nil)
	if len(back.Groups()) != 0 {
		t.Fatal("empty group kept")
	}
}

func TestIssueLinks(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "proj")
	os.MkdirAll(repo, 0o755)
	p := filepath.Join(root, "library.json")
	l, _ := Load(p)
	rules := []issuelink.Rule{{Pattern: `\bPROJ-(\d+)\b`, URL: "https://jira.example.com/browse/PROJ-$1"}}
	if err := l.SetIssueLinks(repo, rules); err == nil {
		t.Fatal("rules set for a repository not in the library")
	}
	l.Opened(repo, time.Unix(1, 0))
	if err := l.SetIssueLinks(repo, rules); err != nil {
		t.Fatal(err)
	}
	again, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.IssueLinks(repo); !slices.Equal(got, rules) {
		t.Fatalf("rules %+v", got)
	}
}
