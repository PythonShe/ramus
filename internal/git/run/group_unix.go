//go:build unix

package run

import (
	"os/exec"
	"syscall"
)

// lower runs the process started at the priority nice 10 gives, which
// what it starts later inherits.
func lower(cmd *exec.Cmd) {
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, cmd.Process.Pid, 10)
}

// stopGroup puts the process in a session of its own, so that nothing it
// starts (ssh, a credential helper, gpg) can prompt on the terminal the
// app was started from, where no one would see it, and makes cancelling
// stop its group, so that all of it stops with git.
func stopGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
}

// joinGroup has nothing to do: the session Setsid made is the group.
func joinGroup(cmd *exec.Cmd) {}

// leaveGroup has nothing to forget.
func leaveGroup(cmd *exec.Cmd) {}
