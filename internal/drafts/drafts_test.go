package drafts

import (
	"os"
	"sync"
	"testing"
	"time"
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
	if _, err := n.Load("/r/bad"); err == nil {
		t.Fatal("a broken file read")
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
