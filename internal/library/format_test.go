package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"ramus/internal/issuelink"
)

// v060 is a library as Ramus 0.6.0 wrote it, every field set.
const v060 = `{
  "repos": [
    {
      "path": "/r/a",
      "name": "a",
      "opened": "2026-11-01T10:00:00+01:00",
      "favourite": true,
      "issueLinks": [
        {
          "pattern": "#(\\d+)",
          "url": "https://example.com/issues/$1"
        }
      ],
      "pinned": [
        "refs/heads/main"
      ],
      "hidden": [
        "refs/remotes/origin/wip"
      ],
      "noFetch": true,
      "editor": "Zed"
    },
    {
      "path": "/r/b",
      "name": "b",
      "opened": "2026-10-01T09:00:00Z"
    }
  ],
  "groups": [
    {
      "name": "work",
      "paths": [
        "/r/a",
        "/r/b"
      ]
    }
  ]
}
`

// A library 0.6.0 wrote reads as it was, and a save writes it back the
// same: the format is one later versions keep reading.
func TestReadsTheLibraryOf060(t *testing.T) {
	p := filepath.Join(t.TempDir(), "library.json")
	os.WriteFile(p, []byte(v060), 0o600)
	l, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	a := l.Recent(1)[0]
	opened, _ := time.Parse(time.RFC3339, "2026-11-01T10:00:00+01:00")
	want := Repo{Path: "/r/a", Name: "a", Opened: opened, Favourite: true,
		IssueLinks: []issuelink.Rule{{Pattern: `#(\d+)`, URL: "https://example.com/issues/$1"}},
		Pinned:     []string{"refs/heads/main"}, Hidden: []string{"refs/remotes/origin/wip"}, NoFetch: true, Editor: "Zed"}
	if !reflect.DeepEqual(a, want) || !a.Opened.Equal(opened) {
		t.Fatalf("read %+v", a)
	}
	if g := l.Groups(); len(g) != 1 || len(g[0].Paths) != 2 {
		t.Fatalf("groups %+v", g)
	}
	if err := l.save(); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(p); string(data) != v060 {
		t.Fatalf("written back as\n%s", data)
	}
}

// Keys a later version adds, to the file, a repository or a group, are
// kept by a save of this one.
func TestKeepsWhatALaterVersionAdded(t *testing.T) {
	p := filepath.Join(t.TempDir(), "library.json")
	os.WriteFile(p, []byte(`{"version": 3, "repos": [{"path": "/r/a", "name": "a", "opened": "2026-11-01T10:00:00Z", "colour": "teal"}],
		"groups": [{"name": "work", "paths": ["/r/a"], "sort": {"by": "name"}}], "sync": true}`), 0o600)
	l, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.SetFavourite("/r/a", true); err != nil {
		t.Fatal(err)
	}
	if err := l.SetGroup("work", []string{"/r/a", "/r/b"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	var f struct {
		Version int
		Sync    bool
		Repos   []map[string]any
		Groups  []map[string]any
	}
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	if f.Version != 3 || !f.Sync || f.Repos[0]["colour"] != "teal" || f.Repos[0]["favourite"] != true ||
		f.Groups[0]["sort"] == nil || len(f.Groups[0]["paths"].([]any)) != 2 {
		t.Fatalf("written:\n%s", data)
	}
}
