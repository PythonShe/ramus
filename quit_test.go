package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ramus/internal/settings"
	"ramus/internal/statefile"
	"ramus/internal/ui"
)

// A quit gives up on a write that hangs at its bound and names it, with
// one that failed; the next quit waits on the same hung write rather
// than start another wait for it; once the write ends, a quit finds it
// done.
func TestQuitGivesUpOnAHungWrite(t *testing.T) {
	ws := &windows{}
	block := make(chan struct{})
	var started atomic.Int32
	waits := []pendingWrite{
		{"hung", "/data/layout.json", func() bool { started.Add(1); <-block; return true }},
		{"done", "/data/trust.json", func() bool { return true }},
		{"failed", "/data/drafts", func() bool { return false }},
	}
	// Long enough for the writes that end to be seen ended, on a machine
	// slow to run their goroutines.
	if left := ws.waitAll(time.Second, waits); !slices.Equal(left, []string{"/data/layout.json", "/data/drafts"}) {
		t.Fatalf("left %q", left)
	}
	if left := ws.waitAll(20*time.Millisecond, waits[:1]); len(left) != 1 || started.Load() != 1 {
		t.Fatalf("left %q, the hung write waited on %d times", left, started.Load())
	}
	close(block)
	if left := ws.waitAll(time.Minute, waits[:2]); len(left) != 0 {
		t.Fatalf("left %q once written", left)
	}
}

// A settings write held past the quit's bound, here by another copy's
// lock, is named in the record the next launch tells once; a later quit
// that writes everything removes the record.
func TestQuitRecordsWhatItGaveUpOn(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")
	unlock, err := statefile.Lock(p, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	k := settings.NewKeeper(p)
	t.Cleanup(k.Close)
	s := settings.Defaults
	s.DiffSplit = true
	k.Keep(s, nil)
	ws := &windows{dataDir: dir, deps: ui.Deps{SettingsKeeper: k}}
	// ended waits for the wait of key, which a quit leaves at its bound.
	ended := func(key string) {
		ws.waitMu.Lock()
		w := ws.waiting[key]
		ws.waitMu.Unlock()
		if w != nil {
			<-w.done
		}
	}
	ws.flush()
	unlock()
	ended("unsaved")
	files := takeUnsaved(dir)
	if !slices.Equal(files, []string{p}) {
		t.Fatalf("recorded %q", files)
	}
	if n := unsavedNotice(files); !strings.Contains(n, p) {
		t.Fatalf("told %q", n)
	}
	if again := takeUnsaved(dir); again != nil {
		t.Fatalf("told twice: %q", again)
	}
	// The next quit, once the first's wait ended, writes the change and
	// records nothing.
	ended("settings")
	ws.flush()
	ended("unsaved")
	if _, err := os.Stat(filepath.Join(dir, unsavedFile)); !os.IsNotExist(err) {
		t.Fatalf("a record left: %v", err)
	}
	if got, _ := settings.Load(p); !got.DiffSplit {
		t.Fatalf("the quit's write did not land: %+v", got)
	}
	ws.waitMu.Lock()
	defer ws.waitMu.Unlock()
	if len(ws.waiting) != 0 {
		t.Fatalf("waits left running: %v", ws.waiting)
	}
}

// The record of an earlier close of the last window, still being written
// as it closes again with everything written, is not left as the last
// word: the next launch would tell of files that were saved.
func TestQuitRecordWrittenOverOneStillBeingWritten(t *testing.T) {
	was := unsavedWait
	unsavedWait = time.Minute
	t.Cleanup(func() { unsavedWait = was })
	dir := t.TempDir()
	ws := &windows{dataDir: dir, told: true}
	block := make(chan struct{})
	ws.start("unsaved", func() bool {
		<-block
		return writeUnsaved(dir, []string{"/data/settings.json"}) == nil
	})
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(block)
	}()
	ws.recordUnsaved(nil)
	if files := takeUnsaved(dir); files != nil {
		t.Fatalf("left recorded as not saved: %q", files)
	}
	if ws.told {
		t.Fatal("a record still taken to stand")
	}
}
