package statefile

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// TestWriteFileIfLeavesAChangedFile has the file change before the
// rename: the write fails with ErrChanged, the file keeps the change, and
// no temporary file is left beside it.
func TestWriteFileIfLeavesAChangedFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")
	if err := WriteFile(p, []byte("1\n")); err != nil {
		t.Fatal(err)
	}
	err := WriteFileIf(p, []byte("2\n"), func() bool {
		os.WriteFile(p, []byte("edited\n"), 0o600)
		return false
	})
	if !errors.Is(err, ErrChanged) {
		t.Fatalf("err = %v, want ErrChanged", err)
	}
	if data, _ := os.ReadFile(p); string(data) != "edited\n" {
		t.Fatalf("file has %q", data)
	}
	if names, _ := os.ReadDir(dir); len(names) != 1 {
		t.Fatalf("left beside the file: %v", names)
	}
}

// A state file's lock keeps a second writer out until the first lets it
// go, and a writer that waited past its bound is told so.
func TestLockKeepsASecondWriterOut(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	unlock, err := Lock(p, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(p, 20*time.Millisecond); !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v, want ErrLocked", err)
	}
	took := make(chan error, 1)
	go func() {
		u, err := Lock(p, time.Minute)
		if err == nil {
			u()
		}
		took <- err
	}()
	unlock()
	if err := <-took; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the lock made the file itself: %v", err)
	}
}

// A later version's keys of a list's elements stay with the element they
// were on, told apart by its key field, when elements are removed or
// moved; with no key field they go by place.
func TestKeepUnknownInAList(t *testing.T) {
	type keyed struct {
		Name string `json:"name" statefile:"key"`
	}
	type placed struct {
		Name string `json:"name"`
	}
	old := json.RawMessage(`[{"name": "A", "icon": "a"}, {"name": "B", "icon": "b"}, {"name": "C", "icon": "c"}]`)
	now := json.RawMessage(`[{"name":"C"},{"name":"new"},{"name":"B"}]`)
	var got []map[string]string
	json.Unmarshal(KeepUnknown(old, now, reflect.TypeFor[[]keyed]()), &got)
	want := []map[string]string{{"name": "C", "icon": "c"}, {"name": "new"}, {"name": "B", "icon": "b"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("by key: %v", got)
	}
	got = nil
	json.Unmarshal(KeepUnknown(old, now, reflect.TypeFor[[]placed]()), &got)
	want = []map[string]string{{"name": "C", "icon": "a"}, {"name": "new", "icon": "b"}, {"name": "B", "icon": "c"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("by place: %v", got)
	}
	// Two elements of one key: each old one is taken once.
	old = json.RawMessage(`[{"name": "A", "icon": "1"}, {"name": "A", "icon": "2"}]`)
	got = nil
	json.Unmarshal(KeepUnknown(old, json.RawMessage(`[{"name":"A"},{"name":"A"}]`), reflect.TypeFor[[]keyed]()), &got)
	if got[0]["icon"] != "1" || got[1]["icon"] != "2" {
		t.Fatalf("one key twice: %v", got)
	}
}

// TestKeptAsideUnderAFreeName keeps two unreadable files in turn: the
// second takes the next name, not the first's.
func TestKeptAsideUnderAFreeName(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	var kept []string
	for _, content := range []string{"{", "[["} {
		os.WriteFile(p, []byte(content), 0o600)
		e := Unreadable(p, errors.New("bad"))
		if e.KeepErr != nil {
			t.Fatal(e.KeepErr)
		}
		kept = append(kept, e.KeptAs)
	}
	if kept[0] != p+".broken" || kept[1] != p+".broken-2" {
		t.Fatalf("kept as %v", kept)
	}
	if data, _ := os.ReadFile(kept[0]); string(data) != "{" {
		t.Fatalf("first kept file has %q", data)
	}
}
