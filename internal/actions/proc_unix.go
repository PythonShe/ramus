//go:build unix

package actions

import (
	"os/exec"
	"syscall"
)

// inGroup has cmd start in a session of its own, which cancelling ends
// whole: an action is often a shell running a build, whose children
// would otherwise run on, orphaned, after the repository closed. Nothing
// it starts can prompt on the terminal the app was started from either.
func inGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
}

// joined has nothing to do: the session Setsid made is the group.
func joined(cmd *exec.Cmd) {}

// ended kills what is left of the group of an action cancelled: a child
// that ignored the request to stop.
func ended(cmd *exec.Cmd, cancelled bool) {
	if cancelled {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
