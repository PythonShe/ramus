//go:build unix

package write

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A named pipe in place of a file git keeps in its directory (commondir,
// MERGE_MSG, a rebase's message) is refused at once, not waited on.
func TestNamedPipesInTheGitDirectory(t *testing.T) {
	gitDir := t.TempDir()
	for _, name := range []string{"commondir", "MERGE_MSG", filepath.Join("rebase-merge", "message"), filepath.Join("rebase-merge", "current-fixups")} {
		p := filepath.Join(gitDir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := syscall.Mkfifo(p, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	steps := []struct {
		what string
		fn   func()
	}{
		{"a lock's place", func() {
			if !inRepository(gitDir, filepath.Join(gitDir, "index.lock")) {
				t.Error("a lock in the git directory is not in it")
			}
		}},
		{"the prepared message", func() {
			if _, err := PreparedMessage(gitDir, "#"); err == nil {
				t.Error("read a named pipe as MERGE_MSG")
			}
		}},
		{"the message kept for a continue", func() {
			if _, err := keepMessage(gitDir); err == nil {
				t.Error("read a named pipe as MERGE_MSG")
			}
		}},
		{"a squash's comment", func() {
			if c := squashComment(gitDir); c != "" {
				t.Errorf("comment %q", c)
			}
		}},
	}
	for _, s := range steps {
		done := make(chan struct{})
		go func() { s.fn(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: still waiting on a named pipe", s.what)
		}
	}
}
