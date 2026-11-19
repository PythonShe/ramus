package library

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"ramus/internal/statefile"
)

// fortyAndAGroup writes a library of 40 repositories and a group to p
// and returns its content.
func fortyAndAGroup(t *testing.T, p string) []byte {
	t.Helper()
	l := &Library{path: p}
	for i := range 40 {
		l.repos = append(l.repos, Repo{Path: fmt.Sprintf("/r/%d", i), Name: fmt.Sprint(i), Opened: time.Now()})
	}
	l.groups = []Group{{Name: "work", Paths: []string{"/r/1", "/r/2"}}}
	if err := l.save(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	return data
}

// A library file that does not read (cut short by a power cut, edited
// by hand) is moved aside, named in the error, before the next
// repository opened writes a library of one over it.
func TestUnreadableLibraryKeptAside(t *testing.T) {
	p := filepath.Join(t.TempDir(), "library.json")
	data := fortyAndAGroup(t, p)
	cut := data[:len(data)-2]
	os.WriteFile(p, cut, 0o600)
	l, err := Load(p)
	var le *statefile.LoadError
	if !errors.As(err, &le) || le.Path != p || le.KeptAs != p+".broken" || len(l.Recent(100)) != 0 {
		t.Fatalf("load: %v", err)
	}
	if err := l.Opened(t.TempDir(), time.Now()); err != nil {
		t.Fatal(err)
	}
	kept, _ := os.ReadFile(p + ".broken")
	if string(kept) != string(cut) {
		t.Fatal("the broken library was not kept as it was")
	}
}

// One value of the wrong type costs that value: every repository and
// group is still there, and a copy of the file as it was is kept.
func TestWrongTypedValueCostsOnlyItself(t *testing.T) {
	p := filepath.Join(t.TempDir(), "library.json")
	os.WriteFile(p, []byte(`{"repos": [
		{"path": "/r/a", "name": "a", "opened": "2026-01-02T03:04:05Z", "pinned": "refs/heads/main"},
		{"path": "/r/b", "name": "b", "opened": "2026-01-02T03:04:05Z", "favourite": true}
	], "groups": [{"name": "work", "paths": ["/r/a"]}]}`), 0o600)
	l, err := Load(p)
	var le *statefile.LoadError
	if !errors.As(err, &le) || !le.Partial || le.KeptAs != p+".broken" {
		t.Fatalf("load: %v", err)
	}
	if n := len(l.Recent(100)); n != 2 || len(l.Groups()) != 1 || len(l.Favourites()) != 1 {
		t.Fatalf("read %d repositories, %d groups", n, len(l.Groups()))
	}
	// Read again before any save: the same copy, not a second.
	if _, err := Load(p); !errors.As(err, &le) || le.KeptAs != p+".broken" {
		t.Fatalf("second load: %v", err)
	}
}

// A library that did not read and could not be kept aside is never
// written.
func TestLibraryNotKeptNotWritten(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("a folder's permissions do not stop this user")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "library.json")
	os.WriteFile(p, []byte("{"), 0o600)
	os.Chmod(dir, 0o500)
	defer os.Chmod(dir, 0o700)
	l, err := Load(p)
	var le *statefile.LoadError
	if !errors.As(err, &le) || le.KeptAs != "" || le.KeepErr == nil {
		t.Fatalf("load: %v", err)
	}
	os.Chmod(dir, 0o700)
	if err := l.Opened(t.TempDir(), time.Now()); err == nil {
		t.Fatal("a library not kept aside was written over")
	}
	if data, _ := os.ReadFile(p); string(data) != "{" {
		t.Fatalf("file now %q", data)
	}
}
