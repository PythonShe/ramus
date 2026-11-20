package drafts

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ramus/internal/statefile"
)

// A draft kept is read back by a new store; an empty one removes it, and
// a repository's draft is never another's.
func TestKeepLoad(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	d := Draft{Subject: "fix the thing", Body: "because\n\nreasons", CoAuthors: "A <a@example.invalid>"}
	s.Keep("/r/one", d)
	if got, err := s.Load("/r/one"); err != nil || got != d {
		t.Fatalf("pending: %+v, %v", got, err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	waitIdle(s)
	n := New(dir)
	if got, err := n.Load("/r/one"); err != nil || got != d {
		t.Fatalf("read back: %+v, %v", got, err)
	}
	if got, err := n.Load("/r/two"); err != nil || !got.Empty() {
		t.Fatalf("other: %+v, %v", got, err)
	}

	// A file under another repository's name is not its draft.
	if err := os.Rename(n.name("/r/one"), n.name("/r/two")); err != nil {
		t.Fatal(err)
	}
	if got, err := n.Load("/r/two"); err != nil || !got.Empty() {
		t.Fatalf("renamed: %+v, %v", got, err)
	}
	if err := os.Rename(n.name("/r/two"), n.name("/r/one")); err != nil {
		t.Fatal(err)
	}

	n.Keep("/r/one", Draft{})
	waitIdle(n)
	if _, err := os.Stat(n.name("/r/one")); !os.IsNotExist(err) {
		t.Fatalf("empty draft left a file: %v", err)
	}
	if err := os.WriteFile(n.name("/r/bad"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := n.Load("/r/bad")
	var le *statefile.LoadError
	if !errors.As(err, &le) || le.KeptAs != n.name("/r/bad")+".broken" {
		t.Fatalf("a broken file read: %v", err)
	}
	// The next draft kept leaves the broken one where it went.
	n.Keep("/r/bad", Draft{Subject: "new"})
	waitIdle(n)
	if kept, _ := os.ReadFile(le.KeptAs); string(kept) != "{" {
		t.Fatalf("kept %q", kept)
	}
}

// A draft as 0.6.0 wrote it reads, and a draft kept keeps what a later
// version added to the file.
func TestDraftFormat(t *testing.T) {
	s := New(t.TempDir())
	os.MkdirAll(s.dir, 0o700)
	os.WriteFile(s.name("/r"), []byte(`{"path":"/r","subject":"Fix","body":"Why","coAuthors":"A <a@x>"}`+"\n"), 0o600)
	if got, err := s.Load("/r"); err != nil || got != (Draft{Subject: "Fix", Body: "Why", CoAuthors: "A <a@x>"}) {
		t.Fatalf("0.6.0 draft: %+v %v", got, err)
	}
	os.WriteFile(s.name("/r"), []byte(`{"path":"/r","subject":"Fix","amend":true}`), 0o600)
	s.Keep("/r", Draft{Subject: "Fix it"})
	waitIdle(s)
	if data, _ := os.ReadFile(s.name("/r")); !strings.Contains(string(data), `"amend":true`) || !strings.Contains(string(data), `"Fix it"`) {
		t.Fatalf("written %s", data)
	}
}

// Many drafts kept at once end as the last one of each repository.
func TestKeepConcurrent(t *testing.T) {
	s := New(t.TempDir())
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Go(func() { s.Keep("/r", Draft{Subject: string(rune('a' + i%26))}) })
	}
	wg.Wait()
	s.Keep("/r", Draft{Subject: "last"})
	waitIdle(s)
	if got, err := New(s.dir).Load("/r"); err != nil || got.Subject != "last" {
		t.Fatalf("%+v, %v", got, err)
	}
}

// A draft moved to a new key stays under its old one until it is
// written under the new: a write that fails (a full disk refuses the
// write and allows the removal) leaves the message where it was, and the
// next write that succeeds moves it.
func TestMoveKeepsTheOldDraftUntilTheNewIsWritten(t *testing.T) {
	for _, flush := range []bool{false, true} {
		s := New(t.TempDir())
		var failed []error
		var mu sync.Mutex
		s.OnError = func(err error) { mu.Lock(); failed = append(failed, err); mu.Unlock() }
		older, key := "/as/given", "/as/resolved"
		d := Draft{Subject: "typed last week"}
		s.Keep(older, d)
		if err := s.Flush(); err != nil {
			t.Fatal(err)
		}
		waitIdle(s)
		// A folder in the place of the new key's file: its write fails.
		if err := os.MkdirAll(filepath.Join(s.name(key), "x"), 0o755); err != nil {
			t.Fatal(err)
		}
		// For Flush to write it, as at a quit, no goroutine may.
		hold := func(on bool) {
			if flush {
				s.mu.Lock()
				s.busy = on
				s.mu.Unlock()
			}
		}
		hold(true)
		s.Move(older, key, d)
		if flush {
			if err := s.Flush(); err == nil {
				t.Fatal("flushed a draft that could not be written")
			}
		}
		hold(false)
		waitIdle(s)
		mu.Lock()
		n := len(failed)
		mu.Unlock()
		if !flush && n != 1 {
			t.Fatalf("%d writes failed: %v", n, failed)
		}
		if got, err := New(s.dir).Load(older); err != nil || got != d {
			t.Fatalf("flush %v: the draft under the older key is %+v, %v once the write under the new one failed", flush, got, err)
		}

		if err := os.RemoveAll(s.name(key)); err != nil {
			t.Fatal(err)
		}
		d.Body = "and edited"
		hold(true)
		s.Keep(key, d)
		if flush {
			if err := s.Flush(); err != nil {
				t.Fatal(err)
			}
		}
		hold(false)
		waitIdle(s)
		if got, err := New(s.dir).Load(key); err != nil || got != d {
			t.Fatalf("flush %v: the new key has %+v, %v", flush, got, err)
		}
		if _, err := os.Stat(s.name(older)); !os.IsNotExist(err) {
			t.Fatalf("flush %v: the older draft is still there once the new one is written: %v", flush, err)
		}
	}
}

// waitIdle waits for the writes kept to be made: busy is cleared once
// the last one is.
func waitIdle(s *Store) {
	for {
		s.mu.Lock()
		busy := s.busy
		s.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(time.Millisecond)
	}
}
