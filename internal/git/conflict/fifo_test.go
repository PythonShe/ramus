//go:build unix

package conflict

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/pathsafe"
)

// A named pipe handed over as a side of a merge is refused, not read: a
// read would wait for a writer for ever.
func TestMergeFilesRefusesANamedPipe(t *testing.T) {
	dir := t.TempDir()
	pipe := filepath.Join(dir, "local")
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Skip(err)
	}
	remote := filepath.Join(dir, "remote")
	if err := os.WriteFile(remote, []byte("theirs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := MergeFiles(context.Background(), testrun.Runner(t), "", pipe, remote, [3]string{"a", "b", "c"})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, pathsafe.ErrNotRegular) {
			t.Fatalf("err %v", err)
		}
	case <-time.After(30 * time.Second):
		// Lets the reader go, so the test ends either way.
		if f, err := os.OpenFile(pipe, os.O_RDWR, 0); err == nil {
			f.Close()
		}
		t.Fatal("waited on the pipe")
	}
}
