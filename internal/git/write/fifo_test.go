//go:build unix

package write

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// An undo that finds a named pipe where the write left an empty file
// refuses, the pipe changed since, and does not wait on it: a read of
// it waits for a writer for ever.
func TestUndoDoesNotReadANamedPipe(t *testing.T) {
	h := newHarness(t)
	h.r.Write("a", "")
	h.r.Commit("one")
	h.r.Write("a", "work\n")
	h.do(Discard(h.w, []string{"a"}), nil, true)
	p := filepath.Join(h.r.Dir, "a")
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skip(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := wait(t, h.q.Undo())
		done <- err
	}()
	select {
	case err := <-done:
		var se *StaleError
		if !errors.As(err, &se) {
			t.Fatalf("err %v", err)
		}
	case <-time.After(time.Minute):
		// Lets every reader go, each finding the pipe empty, so the test
		// ends either way.
		stop := make(chan struct{})
		t.Cleanup(func() { close(stop) })
		go func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				if f, err := os.OpenFile(p, os.O_RDWR, 0); err == nil {
					f.Close()
				}
				runtime.Gosched()
			}
		}()
		t.Fatal("waited on the pipe")
	}
}
