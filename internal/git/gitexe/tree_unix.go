//go:build unix

package gitexe

import (
	"os/exec"
	"syscall"
)

// StopTree has cancelling cmd kill what it started too: a git that is a
// wrapper script, or a login shell, would leave a hung child running.
func StopTree(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
