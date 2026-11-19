package settings

import (
	"errors"
	"testing"
	"time"
)

// retryAt sets the delays between the tries of a write that failed for
// one test.
func retryAt(t *testing.T, first, most time.Duration) {
	f, m := retryFirst, retryMost
	retryFirst, retryMost = first, most
	t.Cleanup(func() { retryFirst, retryMost = f, m })
}

// A write fails, the file does not read; the user mends it by hand and
// the app regains focus, nothing else: the change the write left out is
// in the file all the same, with no further change of the user's to
// carry it.
func TestKeeperWritesAChangeAFailedWriteLeftOut(t *testing.T) {
	retryAt(t, time.Hour, time.Hour) // the reading taken is what writes it
	m := newModel(t, t.TempDir()+"/settings.json")
	hand(t, m.p, `{"theme": `)
	m.keep(noWrap)
	m.k.Wait()
	if m.failures() != 1 {
		t.Fatal("the write into a broken file did not fail")
	}
	hand(t, m.p, `{"emails": ["me@x"]}`)
	m.focus()
	m.k.Wait()
	if s := load(t, m.p); !s.DiffNoWrap || len(s.Emails) != 1 {
		t.Fatalf("the file holds %+v", s)
	}
	if !m.app.DiffNoWrap || len(m.app.Emails) != 1 || m.k.Unsaved() {
		t.Fatalf("the app holds %+v, unsaved %v", m.app, m.k.Unsaved())
	}
}

// failing has the next n saves fail as a full disk does, and returns a
// channel that hears every save tried.
func failing(t *testing.T, n int) <-chan struct{} {
	tried := make(chan struct{}, 64)
	failSave = func() error {
		tried <- struct{}{}
		if n > 0 {
			n--
			return errors.New("no space left on device")
		}
		return nil
	}
	t.Cleanup(func() { failSave = nil })
	return tried
}

// A write that fails a few times, the disk full, is tried again on its
// own until it lands, the failure told once.
func TestKeeperRetriesAFailedWrite(t *testing.T) {
	retryAt(t, time.Millisecond, 4*time.Millisecond)
	m := newModel(t, t.TempDir()+"/settings.json")
	tried := failing(t, 3)
	m.keep(split)
	for range 4 {
		<-tried
	}
	m.k.Wait()
	if s := load(t, m.p); !s.DiffSplit {
		t.Fatalf("the file holds %+v", s)
	}
	if n := m.failures(); n != 1 {
		t.Fatalf("the failure told %d times", n)
	}
	if m.k.Unsaved() {
		t.Fatal("still unsaved once written")
	}
	m.agrees()
}

// A write that fails until the app quits leaves the change unsaved for
// the quit to name; once the disk has room, the quit's write lands it.
func TestKeeperFailureUntilTheQuit(t *testing.T) {
	retryAt(t, time.Hour, time.Hour)
	m := newModel(t, t.TempDir()+"/settings.json")
	tried := failing(t, 2)
	m.keep(split)
	<-tried
	m.k.Wait()
	m.k.SaveNow()
	<-tried
	if !m.k.WaitWrites(time.Minute) || !m.k.Unsaved() {
		t.Fatal("a change the disk refused counted as saved")
	}
	if s := load(t, m.p); s.DiffSplit {
		t.Fatal("written")
	}
	m.k.SaveNow()
	<-tried
	if !m.k.WaitWrites(time.Minute) || m.k.Unsaved() {
		t.Fatal("the quit's write did not land")
	}
	if s := load(t, m.p); !s.DiffSplit {
		t.Fatalf("the file holds %+v", s)
	}
	if n := m.failures(); n != 1 {
		t.Fatalf("the failure told %d times", n)
	}
	// Nothing left to write: a quit writes nothing.
	m.k.SaveNow()
	select {
	case <-tried:
		t.Fatal("a quit wrote settings with nothing unsaved")
	default:
	}
}
