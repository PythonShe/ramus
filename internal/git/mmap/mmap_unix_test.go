//go:build unix

package mmap

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"ramus/internal/git/osfile"
)

// A named pipe in place of a commit-graph is refused, not waited on.
func TestOpenNamedPipe(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "commit-graph")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		m, err := Open(fifo)
		if err == nil {
			m.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, osfile.ErrNotRegular) {
			t.Fatalf("Open: %v", err)
		}
	case <-time.After(time.Minute):
		if w, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			w.Close()
		}
		t.Fatal("Open is waiting on the pipe")
	}
}
