//go:build unix

package message

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/pathsafe"
)

// A commit.template that names a named pipe is refused, not waited on:
// the config is the repository's, from whoever sent it.
func TestTemplateFIFO(t *testing.T) {
	r := gittest.Init(t)
	rn := testrun.Runner(t)
	fifo := filepath.Join(r.Dir, "tpl")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skip("no FIFO:", err)
	}
	r.Git("config", "commit.template", "tpl")
	done := make(chan error, 1)
	go func() {
		_, err := Template(context.Background(), rn, Where{GitDir: r.GitDir(), WorkTree: r.Dir})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, pathsafe.ErrNotRegular) {
			t.Fatalf("read %v", err)
		}
	case <-time.After(time.Minute):
		// A writer lets a reader stuck on the pipe go, so the test ends.
		if w, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			w.Close()
		}
		<-done
		t.Fatal("waited on the named pipe")
	}
}
