package settings

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"ramus/internal/statefile"
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
	defer k.Close()
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
	// The reading taken has the app's change written: waited for, or the
	// write lands on either side of the hand edit below, and a reading
	// made before it is rightly not taken.
	k.Wait()
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
	var le *statefile.LoadError
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

// A settings file kept with the user's dotfiles and linked into place
// stays linked through the app's saves: the file it leads to is the one
// written, and an edit made there later is read.
func TestKeeperWritesThroughALink(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "dotfiles-settings.json")
	hand(t, dest, `{"emails": ["me@x"]}`)
	p := filepath.Join(dir, "settings.json")
	if err := os.Symlink(dest, p); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	m := newModel(t, p)
	m.keep(split)
	m.k.Wait()
	if got, err := os.Readlink(p); err != nil || got != dest {
		t.Fatalf("the link was replaced: %q, %v", got, err)
	}
	if s := load(t, dest); !s.DiffSplit || len(s.Emails) != 1 {
		t.Fatalf("the linked file holds %+v", s)
	}
	hand(t, dest, `{"emails": ["me@x"], "diffSplit": true, "theme": "dark"}`)
	m.focus()
	if m.app.Theme != "dark" {
		t.Fatalf("an edit of the linked file not read: %+v", m.app)
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
	defer k.Close()
	var got error
	k.Keep(Defaults, func(err error) { got = err })
	k.Wait()
	if got == nil {
		t.Fatal("no error for a folder that is a file")
	}
}

// mainThread is a stand-in for the app's main thread: run posts fn to it
// and waits for it to return, as mygo.RunOnMain does.
type mainThread chan func()

func (m mainThread) run(fn func()) {
	done := make(chan struct{})
	m <- func() { fn(); close(done) }
	<-done
}

// A quit waits on the main thread for the settings to be written, while
// the keeper's callbacks wait for the main thread, as the app wires them:
// the wait ends all the same, as each write and check counts as done
// before its callbacks run, and the callbacks run once the main thread is
// free again.
func TestQuitWaitsNotOnCallbacks(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	Save(p, Defaults)
	inApp, _ := Load(p)
	k := NewKeeper(p)
	defer k.Close()
	k.Started(inApp)
	main := make(mainThread)
	heard := make(chan struct{}, 4)
	k.OnFile = func(FromFile) { main.run(func() { heard <- struct{}{} }) }
	k.Check(nil) // the first focus always reads the file
	k.WaitWrites(time.Minute)
	// A write that fails, its error told on the main thread too.
	os.WriteFile(p, []byte(`{"theme": `), 0o600)
	failed := make(chan struct{}, 1)
	k.Keep(inApp, func(error) { main.run(func() { failed <- struct{}{} }) })
	if !k.WaitWrites(time.Minute) {
		t.Fatal("the wait for the writes did not end")
	}
	// The main thread is free again: what waited on it runs.
	for range 2 {
		(<-main)()
	}
	k.Wait()
	if len(heard) != 1 || len(failed) != 1 {
		t.Fatalf("heard %d readings and %d failures", len(heard), len(failed))
	}
}

// WaitWrites gives up after the time it is given while a write runs.
func TestWaitWritesBounded(t *testing.T) {
	k := NewKeeper(filepath.Join(t.TempDir(), "settings.json"))
	k.file.Lock() // a write that cannot end yet
	k.Keep(Defaults, nil)
	if k.WaitWrites(10 * time.Millisecond) {
		t.Fatal("waited for a write that had not ended")
	}
	k.file.Unlock()
	if !k.WaitWrites(time.Minute) {
		t.Fatal("the write did not end")
	}
}
