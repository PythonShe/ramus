//go:build windows

package gitexe

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// Hide starts cmd with no console window. The app is a GUI program with
// no console, and Windows gives a console program it starts, as git, a
// console window of its own, which flashes up and takes the focus; what
// git starts in turn inherits none.
func Hide(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
}
