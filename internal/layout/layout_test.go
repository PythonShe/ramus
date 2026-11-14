package layout

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
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
