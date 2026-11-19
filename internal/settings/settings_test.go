package settings

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
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

// Every number in the file is bounded where it is read: a snapshotDays
// that would overflow as a duration, and wrap to minutes, expired every
// snapshot of dropped work.
func TestNumbersBounded(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(p, []byte(`{"snapshotDays": 213504, "recentLimit": 99999999, "fetchMinutes": 1000000000000,
		"pictureCompare": 9, "historyMessage": -5, "historyColumns": {"graph": {"width": 1000000000}, "hash": {"width": -3, "hidden": true}},
		"tables": {"branches": {"widths": {"name": 300000}}}}`), 0o600)
	s, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.SnapshotDays != MaxSnapshotDays || time.Duration(s.SnapshotDays)*24*time.Hour < 365*24*time.Hour {
		t.Errorf("snapshot days %d", s.SnapshotDays)
	}
	if s.RecentLimit != MaxRecentLimit || s.FetchMinutes != MaxFetchMinutes || time.Duration(s.FetchEvery())*time.Minute <= 0 {
		t.Errorf("recent %d, fetch %d", s.RecentLimit, s.FetchMinutes)
	}
	if s.PictureCompare != 0 || s.HistoryMessage != 0 || s.HistoryColumns["graph"].Width != 0 ||
		s.HistoryColumns["hash"] != (Column{Hidden: true}) || s.Tables != nil {
		t.Errorf("layout numbers %+v %v %+v %+v", s.PictureCompare, s.HistoryMessage, s.HistoryColumns, s.Tables)
	}
}

// v060 is a settings file as Ramus 0.6.0 wrote it.
const v060 = `{
  "actions": [
    {
      "name": "Lint",
      "command": [
        "/usr/bin/make",
        "lint"
      ],
      "on": "file"
    }
  ],
  "editor": [
    "/Applications/Zed.app"
  ],
  "emails": [
    "me@example.com"
  ],
  "historyColumns": {
    "graph": {
      "thin": true
    }
  },
  "keys": {
    "push": [
      "CmdOrCtrl+Shift+U"
    ]
  },
  "recentLimit": 20,
  "snapshotDays": 30,
  "tables": {
    "branches": {
      "order": [
        "time",
        "name"
      ],
      "widths": {
        "name": 200
      }
    }
  },
  "theme": "dark"
}
`

// Settings 0.6.0 wrote read as they were, and a save writes them back
// the same.
func TestReadsTheSettingsOf060(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(p, []byte(v060), 0o600)
	s, err := Load(p)
	if err != nil || s.Theme != "dark" || s.SnapshotDays != 30 || len(s.Actions) != 1 || s.Actions[0].On != "file" ||
		!s.HistoryColumns["graph"].Thin || len(s.Keys["push"]) != 1 || s.Tables["branches"].Widths["name"] != 200 {
		t.Fatalf("read %+v %v", s, err)
	}
	if err := Save(p, s); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(p); string(data) != v060 {
		t.Fatalf("written back as\n%s", data)
	}
}

// What a later version added inside a column, a table and an action
// stays across a save that rewrites them (a column dragged, a table's
// order changed), through Save and through the Keeper, as at the top.
func TestKeepsWhatALaterVersionAddedInside(t *testing.T) {
	const later = `{
  "actions": [{"name": "Lint", "command": ["/usr/bin/make"], "icon": "hammer"}],
  "future": {"a": 1},
  "historyColumns": {"graph": {"width": 80, "glow": true}, "date": {"hidden": true}},
  "tables": {"branches": {"order": ["name"], "sticky": ["name"]}}
}`
	kept := []string{`"icon":"hammer"`, `"future":{"a":1}`, `"glow":true`, `"sticky":["name"]`}
	compact := func(data []byte) string {
		return strings.Join(strings.Fields(strings.ReplaceAll(string(data), `": `, `":`)), "")
	}
	edit := func(s *Settings) {
		s.HistoryColumns = map[string]Column{"graph": {Width: 120}, "date": {Hidden: true}}
		s.Tables = map[string]TableLayout{"branches": {Order: []string{"name", "date"}}}
		s.Actions = slices.Clone(s.Actions)
		s.Actions[0].On = "file"
	}
	t.Run("save", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "settings.json")
		os.WriteFile(p, []byte(later), 0o600)
		s, err := Load(p)
		if err != nil {
			t.Fatal(err)
		}
		edit(&s)
		if err := Save(p, s); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(p)
		for _, k := range kept {
			if !strings.Contains(compact(data), k) {
				t.Errorf("%s dropped:\n%s", k, data)
			}
		}
		if back, _ := Load(p); back.HistoryColumns["graph"].Width != 120 || back.Actions[0].On != "file" ||
			len(back.Tables["branches"].Order) != 2 {
			t.Fatalf("the app's change not written: %+v", back)
		}
	})
	t.Run("keeper", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "settings.json")
		os.WriteFile(p, []byte(later), 0o600)
		m := newModel(t, p)
		m.keep(edit)
		m.agrees()
		data, _ := os.ReadFile(p)
		for _, k := range kept {
			if !strings.Contains(compact(data), k) {
				t.Errorf("%s dropped:\n%s", k, data)
			}
		}
	})
}

// A settings file that does not read at all is moved aside before
// anything is written, so the first save made in the app (a column
// dragged) cannot replace the user's actions, shortcuts and editor with
// the defaults; the error says where the file went.
func TestUnreadableFileKeptAside(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	text := `{"theme": "dark", "actions": [{"name": "Lint", "command": ["/usr/bin/make", "lint"]}], "emails": ["me@x"],}`
	os.WriteFile(p, []byte(text), 0o600)
	s, err := Load(p)
	var le *LoadError
	if !errors.As(err, &le) || le.Partial || le.KeptAs != p+".broken" || le.Path != p || !reflect.DeepEqual(s, Defaults) {
		t.Fatalf("load: %+v %v", s, err)
	}
	s.DiffSplit = true
	if err := Save(p, s); err != nil {
		t.Fatal(err)
	}
	if kept, _ := os.ReadFile(p + ".broken"); string(kept) != text {
		t.Fatalf("kept aside: %q", kept)
	}
	// Broken again: a second copy, the first left alone.
	os.WriteFile(p, []byte("{"), 0o600)
	if _, err := Load(p); !errors.As(err, &le) || le.KeptAs != p+".broken-2" {
		t.Fatalf("second: %v", err)
	}
	if kept, _ := os.ReadFile(p + ".broken"); string(kept) != text {
		t.Fatalf("first copy changed: %q", kept)
	}
}

// A file the app may not read is moved aside too, never replaced unseen.
func TestFileNotOpenedKeptAside(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permissions do not stop this user reading")
	}
	p := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(p, []byte(`{"theme": "dark"}`), 0o600)
	os.Chmod(p, 0)
	_, err := Load(p)
	var le *LoadError
	if !errors.As(err, &le) || le.KeptAs != p+".broken" {
		t.Fatalf("load: %v", err)
	}
	if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("still in place: %v", err)
	}
}

// A field of the wrong type costs that field only: the others load, and
// a save keeps the field as the user wrote it for them to mend.
func TestWrongTypedFieldCostsOnlyItself(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(p, []byte(`{"theme": "dark", "actions": [{"name": "Lint", "command": ["/usr/bin/make", "lint"]}],
		"emails": ["me@x"], "editor": "/usr/bin/vim", "snapshotDays": "long"}`), 0o600)
	s, err := Load(p)
	var le *LoadError
	if !errors.As(err, &le) || !le.Partial || le.KeptAs != "" || !strings.Contains(err.Error(), `"editor"`) || !strings.Contains(err.Error(), `"snapshotDays"`) {
		t.Fatalf("load error %v", err)
	}
	if s.Theme != "dark" || len(s.Actions) != 1 || len(s.Emails) != 1 || s.Editor != nil || s.SnapshotDays != Defaults.SnapshotDays {
		t.Fatalf("loaded %+v", s)
	}
	s.DiffSplit = true
	if err := Save(p, s); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	for _, want := range []string{`"editor": "/usr/bin/vim"`, `"snapshotDays": "long"`, `"diffSplit": true`, `"Lint"`, `"me@x"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("saved file lacks %s:\n%s", want, data)
		}
	}
	// Set in the app, the field is the app's again.
	s.Editor = []string{"/usr/bin/nano"}
	Save(p, s)
	if back, err := Load(p); err == nil || !slices.Equal(back.Editor, s.Editor) {
		t.Fatalf("editor set in the app: %v %v", back.Editor, err)
	}
}

// A save never writes over a file that does not read, as one broken by
// hand while the app runs.
func TestSaveRefusesUnreadableFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(p, []byte(`{"theme": "dark",}`), 0o600)
	if err := Save(p, Defaults); err == nil || !strings.Contains(err.Error(), p) {
		t.Fatalf("save over a broken file: %v", err)
	}
	if data, _ := os.ReadFile(p); string(data) != `{"theme": "dark",}` {
		t.Fatalf("file now %q", data)
	}
}
