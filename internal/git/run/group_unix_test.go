//go:build unix

package run

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"ramus/internal/git/gittest"
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
	sid, err := getsid(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if sid != cmd.Process.Pid {
		t.Fatalf("git is in session %d, not its own (%d)", sid, cmd.Process.Pid)
	}
}

// A cancelled command's hook that ignores SIGTERM is killed, not left to
// run to its end.
func TestCancelKillsWhatIgnoresTerm(t *testing.T) {
	r := gittest.Init(t)
	mark := filepath.Join(t.TempDir(), "ran")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := runner(t, 1).Run(ctx, &Cmd{GitDir: r.GitDir(), WorkTree: r.Dir,
		Config: []string{fmt.Sprintf("alias.s=!trap '' TERM; sleep 3; touch %q", mark)}, Args: []string{"s"}})
	if err == nil {
		t.Fatal("ran to the end")
	}
	time.Sleep(time.Until(start.Add(4 * time.Second)))
	if _, err := os.Stat(mark); err == nil {
		t.Fatal("the hook outlived the cancelled command and ran to its end")
	}
}

// A background command runs at a low priority.
func TestLower(t *testing.T) {
	cmd := exec.Command("sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	lower(cmd)
	// Linux returns 20 - nice, others nice itself: 10 either way.
	if p, err := syscall.Getpriority(syscall.PRIO_PROCESS, cmd.Process.Pid); err != nil || p != 10 {
		t.Fatalf("priority %d (%v), want nice 10", p, err)
	}
}
