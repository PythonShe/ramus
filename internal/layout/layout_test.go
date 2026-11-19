package layout

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"ramus/internal/settings"
)

func TestSaveLoad(t *testing.T) {
	p := filepath.Join(t.TempDir(), "layout.json")
	if ws, err := Load(p); err != nil || ws != nil {
		t.Fatalf("no file: %v, %v", ws, err)
	}
	ws := []Window{
		{Tabs: []Tab{{Path: "/a", Section: "history", Commit: "abc"}, {Path: "/b", File: "x/y"}}, Active: 1},
		{Tabs: []Tab{{Path: ""}}}, // nothing to open: left out
		{Tabs: []Tab{{Path: "/c"}}, Active: 7},
	}
	if err := Save(p, ws); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	want := []Window{ws[0], {Tabs: []Tab{{Path: "/c"}}, Active: 0}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, %v", got, err)
	}
	os.WriteFile(p, []byte("{"), 0o600)
	if ws, err := Load(p); err == nil || ws != nil {
		t.Fatalf("broken file: %v, %v", ws, err)
	}
	os.WriteFile(p, []byte("["+strings.Repeat(" ", maxFile)+"]"), 0o600)
	if _, err := Load(p); err == nil {
		t.Fatal("a file past the bound was read")
	}
}

// A layout as 0.6.0 wrote it, a bare array, still reads; a save writes
// the versioned object, which keeps what a later version adds to it.
func TestLayoutFormats(t *testing.T) {
	p := filepath.Join(t.TempDir(), "layout.json")
	os.WriteFile(p, []byte(`[
  {
    "tabs": [
      {
        "path": "/a",
        "section": "history",
        "commit": "abc",
        "file": "x/y"
      },
      {
        "path": "/b"
      }
    ],
    "active": 1
  }
]
`), 0o600)
	want := []Window{{Tabs: []Tab{{Path: "/a", Section: "history", Commit: "abc", File: "x/y"}, {Path: "/b"}}, Active: 1}}
	ws, err := Load(p)
	if err != nil || !reflect.DeepEqual(ws, want) {
		t.Fatalf("0.6.0 layout: %+v %v", ws, err)
	}
	if err := Save(p, ws); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	var f struct {
		Version int
		Windows []Window
	}
	if err := json.Unmarshal(data, &f); err != nil || f.Version != Version || !reflect.DeepEqual(f.Windows, want) {
		t.Fatalf("written as %s", data)
	}
	os.WriteFile(p, []byte(`{"version": 2, "windows": [{"tabs": [{"path": "/c"}]}], "focused": 0}`), 0o600)
	if ws, err := Load(p); err != nil || len(ws) != 1 || ws[0].Tabs[0].Path != "/c" {
		t.Fatalf("a later layout: %+v %v", ws, err)
	}
	Save(p, want)
	if data, _ := os.ReadFile(p); !strings.Contains(string(data), `"focused": 0`) {
		t.Fatalf("a later version's key lost: %s", data)
	}
}

// A layout file that does not read is moved aside, and the next save
// writes a new one beside it rather than over it; one that cannot be
// moved is not written over.
func TestUnreadableLayoutKeptAside(t *testing.T) {
	p := filepath.Join(t.TempDir(), "layout.json")
	os.WriteFile(p, []byte(`[{"tabs": [{"path": "/a"}]`), 0o600)
	_, err := Load(p)
	var le *settings.LoadError
	if !errors.As(err, &le) || le.KeptAs != p+".broken" {
		t.Fatalf("load: %v", err)
	}
	if err := Save(p, []Window{{Tabs: []Tab{{Path: "/b"}}}}); err != nil {
		t.Fatal(err)
	}
	if kept, _ := os.ReadFile(p + ".broken"); string(kept) != `[{"tabs": [{"path": "/a"}]` {
		t.Fatalf("kept %q", kept)
	}
	os.WriteFile(p, []byte("{"), 0o600)
	if err := Save(p, nil); err == nil {
		t.Fatal("a file that does not read was written over")
	}
	// A tab of the wrong shape costs that tab's value only.
	os.WriteFile(p, []byte(`[{"tabs": [{"path": "/a"}, {"path": "/b", "section": 3}], "active": 1}]`), 0o600)
	ws, err := Load(p)
	if !errors.As(err, &le) || !le.Partial || len(ws) != 1 || len(ws[0].Tabs) != 2 {
		t.Fatalf("partly read: %+v %v", ws, err)
	}
}

// The last layout asked for is the one on disk, and a flush wins over
// writes still pending.
func TestKeeper(t *testing.T) {
	p := filepath.Join(t.TempDir(), "layout.json")
	k := NewKeeper(p)
	var mu sync.Mutex
	var errs []error
	k.OnError = func(err error) { mu.Lock(); errs = append(errs, err); mu.Unlock() }
	for i := range 50 {
		k.Keep([]Window{{Tabs: []Tab{{Path: "/" + strings.Repeat("x", i+1)}}}})
	}
	final := []Window{{Tabs: []Tab{{Path: "/final"}}}}
	if err := k.Flush(final); err != nil {
		t.Fatal(err)
	}
	// Let anything still running finish.
	k.Keep(final)
	for {
		k.mu.Lock()
		busy := k.busy
		k.mu.Unlock()
		if !busy {
			break
		}
	}
	got, err := Load(p)
	if err != nil || !reflect.DeepEqual(got, final) || len(errs) != 0 {
		t.Fatalf("got %+v, %v; errors %v", got, err, errs)
	}
}
