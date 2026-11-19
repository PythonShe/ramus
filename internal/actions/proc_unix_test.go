//go:build unix

package actions

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Cancelling an action, as its repository closing does, ends what it
// started too: a shell's child, here one that ignores the request to
// stop, does not run on orphaned.
func TestCancelEndsWhatTheActionStarted(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	pid := 0
	done := make(chan error, 1)
	go func() {
		done <- RunIn(ctx, []string{sh, "-c", "trap '' TERM; sleep 300 & echo $!; wait"}, t.TempDir(), nil, func(l string) {
			mu.Lock()
			pid, _ = strconv.Atoi(l)
			mu.Unlock()
		})
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		p := pid
		mu.Unlock()
		if p > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the action did not start its child")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("a cancelled action succeeded")
	}
	for deadline = time.Now().Add(5 * time.Second); ; {
		// Its parent, the shell, is gone: the child is reaped by init, or
		// is a zombie no signal reaches.
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		if out, _ := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output(); len(out) > 0 && out[0] == 'Z' {
			return
		}
		if time.Now().After(deadline) {
			syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("the action's child %d runs on", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
