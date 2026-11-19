//go:build unix

package snapshot

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"ramus/internal/git/gittest"
)

// A worktrees folder that is a named pipe holds no worktree, and is not
// waited on for a writer.
func TestWorktreesThatIsAPipe(t *testing.T) {
	r := gittest.Init(t)
	fifo := filepath.Join(r.GitDir(), "worktrees")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skip("no FIFO:", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- linkedWorktrees(r.GitDir(), func(admin, _ string) { t.Errorf("a worktree at %s", admin) })
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		// A writer lets an open stuck on the pipe go, so the test ends.
		if w, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			w.Close()
		}
		<-done
		t.Fatal("waited on the named pipe")
	}
}
