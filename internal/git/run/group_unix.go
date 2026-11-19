//go:build unix

package run

import (
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// lower runs the process started at the priority nice 10 gives, which
// what it starts later inherits.
func lower(cmd *exec.Cmd) {
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, cmd.Process.Pid, 10)
}

// killAfter is how long a group cancelled has to end on SIGTERM before it
// is killed: less than WaitDelay, after which exec stops waiting for what
// holds git's output.
const killAfter = 1500 * time.Millisecond

// waiting is a command Run waits on, until Wait has returned.
type waiting struct {
	mu   sync.Mutex
	done bool
}

// waits holds the waiting of each command Run started and waits on.
var waits sync.Map // *exec.Cmd -> *waiting

// stopGroup puts the process in a session of its own, so that nothing it
// starts (ssh, a credential helper, gpg) can prompt on the terminal the
// app was started from, where no one would see it, and makes cancelling
// stop its group, so that all of it stops with git: SIGTERM, then, for a
// command Run waits on that has not ended by killAfter, SIGKILL, so that
// a hook that ignores SIGTERM does not outlive it.
func stopGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		pid := cmd.Process.Pid
		if v, ok := waits.Load(cmd); ok {
			w := v.(*waiting)
			time.AfterFunc(killAfter, func() {
				w.mu.Lock()
				defer w.mu.Unlock()
				// Until Wait returns, something of the group still holds
				// git's output, so the group, and its id, still exist.
				if !w.done {
					_ = syscall.Kill(-pid, syscall.SIGKILL)
				}
			})
		}
		return syscall.Kill(-pid, syscall.SIGTERM)
	}
}

// watchGroup marks cmd, before it starts, as one Run waits on; waited is
// called once Wait has returned, after which the group id, which may then
// be reused, is never signalled.
func watchGroup(cmd *exec.Cmd) (waited func()) {
	w := &waiting{}
	waits.Store(cmd, w)
	return func() {
		w.mu.Lock()
		w.done = true
		w.mu.Unlock()
		waits.Delete(cmd)
	}
}

// joinGroup has nothing to do: the session Setsid made is the group.
func joinGroup(cmd *exec.Cmd) {}

// leaveGroup has nothing to forget.
func leaveGroup(cmd *exec.Cmd) {}
