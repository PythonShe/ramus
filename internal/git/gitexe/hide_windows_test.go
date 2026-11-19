//go:build windows

package gitexe

import (
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

// Hide asks for no console window and keeps the flags already set.
func TestHide(t *testing.T) {
	cmd := exec.Command("git", "version")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	Hide(cmd)
	a := cmd.SysProcAttr
	if !a.HideWindow || a.CreationFlags&windows.CREATE_NO_WINDOW == 0 || a.CreationFlags&windows.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Fatalf("SysProcAttr %+v", a)
	}
	if err := cmd.Run(); err != nil {
		t.Skip(err)
	}
}
