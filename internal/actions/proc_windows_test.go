package actions

import (
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

// An action starts without a console window of its own, which the app,
// having none, would otherwise have Windows open for it.
func TestActionHasNoConsoleWindow(t *testing.T) {
	cmd := exec.Command(`C:\Windows\System32\cmd.exe`, "/c", "exit")
	inGroup(cmd)
	defer ended(cmd, false)
	if a := cmd.SysProcAttr; a == nil || !a.HideWindow || a.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Fatalf("started with %+v", cmd.SysProcAttr)
	}
}
