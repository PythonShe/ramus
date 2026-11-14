//go:build unix

package forge

import (
	"os/exec"
	"syscall"
)

// ownGroup runs the tool in a session of its own, with no terminal to
// ask on, and stops all of it when cancelled.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
}
