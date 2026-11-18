package settings

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadSave(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "settings.json")
	s, err := Load(p)
	if err != nil || !reflect.DeepEqual(s, Defaults) {
		t.Fatalf("missing file: %+v %v", s, err)
	}
	os.MkdirAll(filepath.Dir(p), 0o700)
	os.WriteFile(p, []byte(`{"theme": "dark", "future": {"x": 1}, "snapshotDays": -3}`), 0o600)
	s, err = Load(p)
	if err != nil || s.Theme != "dark" || s.SnapshotDays != 14 {
		t.Fatalf("partial file: %+v %v", s, err)
	}
	s.Editor = []string{"zed", "--wait"}
	if err := Save(p, s); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	if !strings.Contains(string(data), `"future"`) {
		t.Fatalf("unknown key lost: %s", data)
	}
	back, err := Load(p)
	if err != nil || !reflect.DeepEqual(back, s) {
		t.Fatalf("round trip %+v %v", back, err)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(p), ".settings.json.*")); len(left) != 0 {
		t.Fatalf("temporary files left: %q", left)
	}

	os.WriteFile(p, []byte("{\n  \"theme\": \"light\",\n  oops\n}"), 0o600)
	s, err = Load(p)
	if err == nil || !strings.Contains(err.Error(), "line 3") || !reflect.DeepEqual(s, Defaults) {
		t.Fatalf("broken file: %+v %v", s, err)
	}
	s.Theme = "light"
	if err := Save(p, s); err != nil {
		t.Fatal(err)
	}
	if s, err := Load(p); err != nil || s.Theme != "light" {
		t.Fatalf("broken file replaced: %+v %v", s, err)
	}
}

// Fetching in the background is on unless turned off, every
// DefaultFetchMinutes unless the user set at least MinFetchMinutes.
func TestFetchMinutes(t *testing.T) {
	for in, want := range map[int]int{-3: DefaultFetchMinutes, 0: DefaultFetchMinutes, 1: MinFetchMinutes, MinFetchMinutes: MinFetchMinutes, 60: 60} {
		if got := (Settings{FetchMinutes: in}).WithDefaults().FetchEvery(); got != want {
			t.Errorf("%d minutes: got %d, want %d", in, got, want)
		}
	}
	if got := (Settings{NoFetch: true, FetchMinutes: 30}).FetchEvery(); got != 0 {
		t.Errorf("turned off, fetching every %d minutes", got)
	}
}

// A field turned back to its default leaves the file, whichever field.
func TestSaveClearsEveryField(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	on := Settings{Theme: "dark", Editor: []string{"e"}, Terminal: []string{"t"}, SnapshotDays: 3, RecentLimit: 4,
		Actions: []Action{{Name: "a", Command: []string{"/bin/true"}}}, Emails: []string{"x@example.com"},
		DiffSplit: true, DiffNoWrap: true, ReviewUnified: true, PictureCompare: 2, SVGAsPicture: true, ShowDependencyFolders: true, NoFocusRefresh: true, NoFetch: true, FetchMinutes: 10, PullRequests: true, HiddenSections: []string{"tags"},
		NoSidebar: true, Keys: map[string][]string{"push": {"CmdOrCtrl+Shift+U"}}, CloneParent: "/projects",
		HistoryColumns: map[string]Column{"hash": {Width: 80, Hidden: true}, "graph": {Thin: true}}, HistoryMessage: 300,
		Tables: map[string]TableLayout{"branches": {Order: []string{"time", "name"}, Widths: map[string]float32{"name": 200}}}}
	// Every field set: a field added later without a value here fails.
	v := reflect.ValueOf(on)
	for i := range v.NumField() {
		if v.Field(i).IsZero() {
			t.Fatalf("field %s not set in the test", v.Type().Field(i).Name)
		}
	}
	if err := Save(p, on); err != nil {
		t.Fatal(err)
	}
	if err := Save(p, Defaults); err != nil {
		t.Fatal(err)
	}
	if back, err := Load(p); err != nil || !reflect.DeepEqual(back, Defaults) {
		t.Fatalf("back %+v %v", back, err)
	}
}

// A table's layout keeps what a column can take: a width edited by hand
// to nothing goes, and a table left with nothing goes too.
func TestTablesNormalized(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(p, []byte(`{"tables": {
		"branches": {"order": ["time", "name"], "widths": {"name": 200, "time": 0, "upstream": -5}},
		"reflog": {"widths": {"message": -1}},
		"stashes": {}
	}}`), 0o600)
	s, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]TableLayout{"branches": {Order: []string{"time", "name"}, Widths: map[string]float32{"name": 200}}}
	if !reflect.DeepEqual(s.Tables, want) {
		t.Fatalf("tables %+v", s.Tables)
	}
	if err := Save(p, s); err != nil {
		t.Fatal(err)
	}
	if back, err := Load(p); err != nil || !reflect.DeepEqual(back, s) {
		t.Fatalf("round trip %+v %v", back, err)
	}
}
