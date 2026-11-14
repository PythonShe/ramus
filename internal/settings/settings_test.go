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

func TestFetchMinutes(t *testing.T) {
	for in, want := range map[int]int{-3: 0, 0: 0, 1: MinFetchMinutes, MinFetchMinutes: MinFetchMinutes, 60: 60} {
		if got := (Settings{FetchMinutes: in}).WithDefaults().FetchMinutes; got != want {
			t.Errorf("%d minutes: got %d, want %d", in, got, want)
		}
	}
}

// A field turned back to its default leaves the file, whichever field.
func TestSaveClearsEveryField(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	on := Settings{Theme: "dark", Editor: []string{"e"}, Terminal: []string{"t"}, SnapshotDays: 3, RecentLimit: 4,
		Actions: []Action{{Name: "a", Command: []string{"/bin/true"}}}, Emails: []string{"x@example.com"},
		DiffSplit: true, DiffWrap: true, NoFocusRefresh: true, FetchMinutes: 10, HiddenSections: []string{"tags"},
		NoSidebar: true, NoStatusBar: true}
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
