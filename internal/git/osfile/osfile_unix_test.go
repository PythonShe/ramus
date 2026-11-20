//go:build unix

package osfile

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A named pipe, a device or a socket where a file is expected is refused
// at once, not waited on or read without end, also behind a symlink.
func TestRefusesWhatIsNoFile(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "HEAD")
	if err := os.Symlink(fifo, link); err != nil {
		t.Fatal(err)
	}
	// A socket path must be short; the temporary folder may not be.
	sockDir, err := os.MkdirTemp("", "osf")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sockDir)
	sock := filepath.Join(sockDir, "s")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	opens := map[string]func(string) error{
		"Open": func(p string) error {
			f, err := Open(p)
			if err == nil {
				f.Close()
			}
			return err
		},
		"ReadFile": func(p string) error { _, err := ReadFile(p, 1<<20); return err },
	}
	for name, open := range opens {
		for _, p := range []string{fifo, link, "/dev/zero", "/dev/null", sock} {
			done := make(chan error, 1)
			go func() { done <- open(p) }()
			select {
			case err := <-done:
				if !errors.Is(err, ErrNotRegular) {
					t.Errorf("%s(%s): %v", name, p, err)
				}
			case <-time.After(time.Minute):
				// Release the reader the pipe holds, so that the run ends.
				if w, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
					w.Close()
				}
				t.Fatalf("%s(%s): still waiting", name, p)
			}
		}
	}
}
