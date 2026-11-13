//go:build unix

package run

import (
	"os/exec"
	"syscall"
)

// stopGroup puts the process in a group of its own and makes cancelling
// stop the group, so that what git started (ssh, hooks, a credential
// helper) stops with it.
func stopGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
}
