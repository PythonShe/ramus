package main

import (
	"runtime"
	"testing"

	"github.com/egoist/mygo"
)

// The keys of the menu's items with a role are found where a window
// takes keys before them: Window > Close Window's and Minimize's off
// macOS, none of a hidden menu's, and none at all on macOS.
func TestRoleKeys(t *testing.T) {
	m := mygo.NewMenu([]*mygo.MenuItem{
		{Label: "Hidden", Hidden: true, Submenu: []*mygo.MenuItem{{Role: mygo.RoleQuit, Accelerator: "CmdOrCtrl+Q"}}},
		{Label: "File", Submenu: []*mygo.MenuItem{{Label: "Mine", Accelerator: "CmdOrCtrl+K"}}},
		{Role: mygo.RoleWindowMenu},
	})
	got := roleKeys(m)
	if runtime.GOOS == "darwin" {
		if got != nil {
			t.Fatalf("on macOS: %v", got)
		}
		return
	}
	if got["CmdOrCtrl+W"] == "" || got["CmdOrCtrl+M"] == "" || len(got) != 2 {
		t.Fatalf("role keys %v", got)
	}
}
