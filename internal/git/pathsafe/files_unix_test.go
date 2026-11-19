//go:build !windows

package pathsafe

import (
	"errors"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A named pipe or a device where a file is expected is refused at once,
// not waited on or read without end.
func TestReadFileRefusesWhatIsNoFile(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{fifo, "/dev/zero"} {
		done := make(chan error, 1)
		go func() { _, err := ReadFile(p, 1<<20); done <- err }()
		select {
		case err := <-done:
			if !errors.Is(err, ErrNotRegular) {
				t.Errorf("%s: %v", p, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: still reading", p)
		}
	}
}
