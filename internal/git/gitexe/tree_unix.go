//go:build unix

package gitexe

import (
	"os/exec"
	"syscall"
)

// stopTree has cancelling cmd stop what it started too: a git that is a
// wrapper script would leave its hung child running.
func stopTree(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
