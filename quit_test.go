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
	if left := ws.waitAll(20*time.Millisecond, waits); !slices.Equal(left, []string{"/data/layout.json", "/data/drafts"}) {
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
	ws.flush()
	unlock()
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
	ws.waitMu.Lock()
	w := ws.waiting["settings"]
	ws.waitMu.Unlock()
	if w != nil {
		<-w.done
	}
	ws.flush()
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
