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
