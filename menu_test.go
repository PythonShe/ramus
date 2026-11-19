package main

import (
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo"

	"ramus/internal/settings"
	"ramus/internal/ui"
	"ramus/locales"
)

// The keys of the menu's items with a role are found where a window
// takes keys before them: off macOS, Minimize's and Toggle Full
// Screen's of the menu the app builds, and none of a hidden menu's;
// none at all on macOS.
func TestRoleKeys(t *testing.T) {
	m := mygo.NewMenu([]*mygo.MenuItem{
		{Label: "Hidden", Hidden: true, Submenu: []*mygo.MenuItem{{Role: mygo.RoleQuit, Accelerator: "CmdOrCtrl+Q"}}},
		{Label: "File", Submenu: []*mygo.MenuItem{{Label: "Mine", Accelerator: "CmdOrCtrl+K"}}},
		{Role: mygo.RoleWindowMenu, Submenu: []*mygo.MenuItem{{Role: mygo.RoleMinimize}}},
	})
	got := roleKeys(m)
	if runtime.GOOS == "darwin" {
		if got != nil {
			t.Fatalf("on macOS: %v", got)
		}
		if got := roleKeys(menu(&windows{deps: ui.Deps{Settings: settings.Defaults}})); got != nil {
			t.Fatalf("the app's menu on macOS: %v", got)
		}
		return
	}
	if got["CmdOrCtrl+M"] == "" || len(got) != 1 {
		t.Fatalf("role keys %v", got)
	}
	got = roleKeys(menu(&windows{deps: ui.Deps{Settings: settings.Defaults}}))
	for _, acc := range []string{"CmdOrCtrl+M", "F11"} {
		if got[acc] == "" {
			t.Errorf("no role item on %s: %v", acc, got)
		}
	}
	for _, acc := range []string{"CmdOrCtrl+W", "CmdOrCtrl+R", "Shift+CmdOrCtrl+R", "Ctrl+Shift+I", "CmdOrCtrl+Plus"} {
		if got[acc] != "" {
			t.Errorf("a role item on %s: %v", acc, got)
		}
	}
}

// The menu the app builds, with the shortcuts it ships with on this
// system (CI runs it on macOS, Linux and Windows): every key an item
// shows runs that item. No key is on two items, and none on an item
// with a role is a command's: off macOS the window would take it first
// and run the command, on macOS the item would. Undo and Redo of the
// Edit menu share the app's Undo and Redo keys on purpose: mygo hands
// both the key and the item to the window, where a field's undo or the
// app's runs. No item closes the window on Close Tab's key, which would
// close every tab while a dialog is open off macOS, where Close Tab's
// own item does nothing then, nor offers what does nothing in a window
// of native UI.
func TestMenuKeys(t *testing.T) {
	m := menu(&windows{deps: ui.Deps{Settings: settings.Defaults}})
	commands := map[string]string{}
	for id, keys := range ui.DefaultKeys() {
		for _, k := range keys {
			commands[chordOf(k)] = id
		}
	}
	shared := map[mygo.MenuRole]string{mygo.RoleUndo: "undo", mygo.RoleRedo: "redo"}
	idle := []mygo.MenuRole{mygo.RoleClose, mygo.RoleReload, mygo.RoleForceReload, mygo.RoleToggleDevTools,
		mygo.RoleResetZoom, mygo.RoleZoomIn, mygo.RoleZoomOut}
	shown := map[string]string{}
	var walk func([]*mygo.MenuItem)
	walk = func(items []*mygo.MenuItem) {
		for _, it := range items {
			if it.Hidden || it.Type == mygo.MenuItemSeparator {
				continue
			}
			if slices.Contains(idle, it.Role) {
				t.Errorf("the menu offers %q (%s)", it.Label, it.Role)
			}
			if it.Submenu != nil {
				walk(it.Submenu)
				continue
			}
			if it.Accelerator == "" {
				continue
			}
			k := chordOf(it.Accelerator)
			if other, ok := shown[k]; ok {
				t.Errorf("%s is on %q and %q", it.Accelerator, other, it.Label)
			}
			shown[k] = it.Label
			if id, ok := commands[k]; ok && it.Role != "" && shared[it.Role] != id {
				t.Errorf("%q (%s) shows %s, which runs %s", it.Label, it.Role, it.Accelerator, id)
			}
		}
	}
	walk(m.Items())
	if got := shown[chordOf("CmdOrCtrl+W")]; got != locales.MenuCloseTab() {
		t.Errorf("Close Tab's key is on %q", got)
	}
	if got := shown[chordOf("CmdOrCtrl+Shift+W")]; got != locales.MenuCloseWindow() {
		t.Errorf("Close Window's key is on %q", got)
	}
}

// chordOf is accelerator a as this system reads it, its modifiers in
// one order, as mygo's roles and the app's shortcuts spell them each
// their own way.
func chordOf(a string) string {
	parts := strings.Split(strings.ToLower(a), "+")
	key := parts[len(parts)-1]
	if key == "plus" {
		key = "=" // mygo's Plus is the key of = and +
	}
	var mods []string
	for _, p := range parts[:len(parts)-1] {
		switch p {
		case "cmdorctrl", "commandorcontrol":
			p = "ctrl"
			if runtime.GOOS == "darwin" {
				p = "super"
			}
		case "cmd", "command", "meta":
			p = "super"
		case "control":
			p = "ctrl"
		case "option":
			p = "alt"
		}
		if !slices.Contains(mods, p) {
			mods = append(mods, p)
		}
	}
	slices.Sort(mods)
	return strings.Join(append(mods, key), "+")
}
