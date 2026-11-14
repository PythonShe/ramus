//go:build unix

package run

import (
	"context"
	"syscall"
	"testing"
)

// git runs as the leader of a session of its own: it has no terminal to
// prompt on, whatever the app was started from.
func TestOwnSession(t *testing.T) {
	r := runner(t, 1)
	cmd := r.Command(context.Background(), &Cmd{GitDir: t.TempDir(), Config: []string{"alias.wait=!sleep 5"}, Args: []string{"wait"}})
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Cancel(); cmd.Wait() }()
	sid, err := syscall.Getsid(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if sid != cmd.Process.Pid {
		t.Fatalf("git is in session %d, not its own (%d)", sid, cmd.Process.Pid)
	}
}
