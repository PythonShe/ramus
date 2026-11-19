//go:build unix

package run

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A commondir that is a named pipe is passed over, not waited on: the
// file is the repository's, from whoever sent it.
func TestCommonDirFIFO(t *testing.T) {
	gitDir := t.TempDir()
	fifo := filepath.Join(gitDir, "commondir")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skip("no FIFO:", err)
	}
	done := make(chan string, 1)
	go func() { done <- commonDir(gitDir) }()
	select {
	case got := <-done:
		if got != filepath.Clean(gitDir) {
			t.Fatalf("common dir %s", got)
		}
	case <-time.After(30 * time.Second):
		// A writer lets a reader stuck on the pipe go, so the test ends.
		if w, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			w.Close()
		}
		<-done
		t.Fatal("waited on the named pipe")
	}
}
