//go:build windows

package run

import (
	"context"
	"testing"

	"golang.org/x/sys/windows"
)

// git starts with no console window: the app has no console, so Windows
// would give each git one of its own.
func TestNoConsoleWindow(t *testing.T) {
	r := runner(t, 1)
	for _, c := range []*Cmd{{GitDir: t.TempDir(), Read: true, Args: []string{"version"}}, {GitDir: t.TempDir(), Args: []string{"version"}}} {
		cmd := r.command(context.Background(), c)
		leaveGroup(cmd)
		a := cmd.SysProcAttr
		if a == nil || !a.HideWindow || a.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
			t.Fatalf("%v: SysProcAttr %+v", c.Args, a)
		}
	}
}
