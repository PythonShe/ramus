package settings

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestKeeperKeepsTheLatest has saves asked for back to back, from two
// goroutines as two windows would: the file ends with the last of them,
// never an older one written after it.
func TestKeeperKeepsTheLatest(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	k := NewKeeper(p)
	var mu sync.Mutex
	last := 0
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			for range 100 {
				mu.Lock()
				last++
				s := Defaults
				s.SnapshotDays = last
				k.Keep(s, func(err error) { t.Error(err) })
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	k.Wait()
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.SnapshotDays != last {
		t.Fatalf("file has %d, the last kept was %d", got.SnapshotDays, last)
	}
}

// An action added to the file by hand while the app runs survives the
// app's next save (a column dragged), and is handed to the app to put in
// force.
func TestHandEditKeptBySave(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	Save(p, Defaults)
	inApp, _ := Load(p)
	k := NewKeeper(p)
	k.Started(inApp)
	var heard []FromFile
	var mu sync.Mutex
	k.OnFile = func(f FromFile) { mu.Lock(); heard = append(heard, f); mu.Unlock() }
	os.WriteFile(p, []byte(`{"theme": "system", "actions": [{"name": "Lint", "command": ["/usr/bin/make", "lint"]}]}`), 0o600)
	inApp.DiffNoWrap = true
	k.Keep(inApp, func(err error) { t.Error(err) })
	k.Wait()
	after, err := Load(p)
	if err != nil || len(after.Actions) != 1 || !after.DiffNoWrap {
		t.Fatalf("after the app's save: %+v %v", after, err)
	}
	if len(heard) != 1 {
		t.Fatalf("heard %d", len(heard))
	}
	s, changed := k.Take(heard[0], inApp)
	if !changed || len(s.Actions) != 1 || !s.DiffNoWrap {
		t.Fatalf("taken %+v %v", s, changed)
	}
	// A setting the app changes later is the app's; the hand edit stays.
	s.DiffSplit = true
	k.Keep(s, nil)
	k.Wait()
	if after, _ := Load(p); len(after.Actions) != 1 || !after.DiffSplit {
		t.Fatalf("second save: %+v", after)
	}
}

// Focus reads the file again: a hand edit is handed to the app, merged
// with what the app changed that is not written yet; an older reading is
// not taken over a newer, and a file that does not read is said once.
func TestCheckReadsHandEdits(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	Save(p, Defaults)
	inApp, _ := Load(p)
	k := NewKeeper(p)
	k.Started(inApp)
	got := make(chan FromFile, 4)
	k.OnFile = func(f FromFile) { got <- f }
	var errs []error
	var mu sync.Mutex
	onError := func(err error) { mu.Lock(); errs = append(errs, err); mu.Unlock() }

	os.WriteFile(p, []byte(`{"emails": ["me@x"]}`), 0o600)
	k.Check(onError)
	k.Wait()
	first := <-got
	cur := inApp
	cur.NoSidebar = true // changed in the app meanwhile
	s, changed := k.Take(first, cur)
	if !changed || len(s.Emails) != 1 || !s.NoSidebar {
		t.Fatalf("taken %+v", s)
	}
	os.WriteFile(p, []byte(`{"emails": ["me@x", "work@x"]}`), 0o600)
	k.Check(onError)
	k.Wait()
	second := <-got
	if s, _ = k.Take(second, s); len(s.Emails) != 2 {
		t.Fatalf("second %+v", s.Emails)
	}
	if _, changed := k.Take(first, s); changed {
		t.Fatal("an older reading taken over a newer")
	}
	// Unchanged: nothing to hear.
	k.Check(onError)
	k.Wait()
	if len(got) != 0 {
		t.Fatal("heard a file that did not change")
	}
	os.WriteFile(p, []byte(`{"emails": [}`), 0o600)
	k.Check(onError)
	k.Check(onError)
	k.Wait()
	var le *LoadError
	if len(errs) != 1 || !errors.As(errs[0], &le) || le.Path != p || len(got) != 0 {
		t.Fatalf("errors %v", errs)
	}
	// Saves wait for the file to read again.
	var saveErr error
	k.Keep(s, func(err error) { saveErr = err })
	k.Wait()
	if data, _ := os.ReadFile(p); saveErr == nil || string(data) != `{"emails": [}` {
		t.Fatalf("saved over a broken file: %v %s", saveErr, data)
	}
}

// TestKeeperReportsFailure says why a write failed to whoever asked for
// it.
func TestKeeperReportsFailure(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	k := NewKeeper(filepath.Join(blocker, "settings.json"))
	var got error
	k.Keep(Defaults, func(err error) { got = err })
	k.Wait()
	if got == nil {
		t.Fatal("no error for a folder that is a file")
	}
}
