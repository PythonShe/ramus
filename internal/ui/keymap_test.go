package ui

import (
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"ramus/locales"
)

func TestChords(t *testing.T) {
	for s, want := range map[string]chord{
		"CmdOrCtrl+Shift+P": {ui.Cmd | ui.Shift, ui.KeyP},
		"cmdorctrl+,":       {ui.Cmd, ui.KeyComma},
		"Alt+F5":            {ui.Alt, ui.KeyF5},
		"Ctrl+Alt+Shift+[":  {ui.Ctrl | ui.Alt | ui.Shift, ui.KeyBracketLeft},
		"Shift+PageDown":    {ui.Shift, ui.KeyPageDown},
		"F12":               {0, ui.KeyF12},
		"Super+Return":      {ui.Super, ui.KeyEnter},
	} {
		k, err := parseChord(s)
		if err != nil || k != want {
			t.Errorf("%q: %+v %v, want %+v", s, k, err, want)
			continue
		}
		if back, err := parseChord(k.String()); err != nil || back != k {
			t.Errorf("%q: %q does not read back: %+v %v", s, k.String(), back, err)
		}
		if k.label() == "" {
			t.Errorf("%q: no label", s)
		}
	}
	for _, s := range []string{"", "Hyper+X", "CmdOrCtrl+Nope", "CmdOrCtrl+", "Shift"} {
		if _, err := parseChord(s); err == nil {
			t.Errorf("%q parsed", s)
		}
	}
	// Every default and every key named reads back as itself.
	for k := range keyNames {
		c := chord{ui.Cmd | ui.Shift, k}
		if back, err := parseChord(c.String()); err != nil || back != c {
			t.Errorf("%q: %+v %v", c.String(), back, err)
		}
	}
	for id, keys := range defaultKeys {
		if !slices.ContainsFunc(bindable, func(b bindableCommand) bool { return b.id == id }) {
			t.Errorf("default for %q, which no command has", id)
		}
		for _, s := range keys {
			if _, err := parseChord(s); err != nil {
				t.Errorf("%s: %v", id, err)
			}
		}
	}
	// What a text field takes for itself goes to it.
	if (chord{ui.Cmd, ui.KeyZ}).textSafe() || (chord{0, ui.KeyF5}).textSafe() || (chord{ui.Alt, ui.KeyLeft}).textSafe() {
		t.Error("a field's own key taken")
	}
	if !(chord{ui.Cmd, ui.KeyEnter}).textSafe() || !(chord{ui.Cmd | ui.Shift, ui.KeyP}).textSafe() {
		t.Error("a shortcut withheld from a field")
	}
	// A chord that types a character goes to the field too: Option on
	// macOS, AltGr on Windows, which comes as Control and Alt.
	for _, c := range []struct {
		goos string
		k    chord
		safe bool
	}{
		{"darwin", chord{ui.Alt, ui.KeyS}, false},
		{"darwin", chord{ui.Alt | ui.Shift, ui.Key2}, false},
		{"darwin", chord{ui.Alt, ui.KeyF5}, true},
		{"darwin", chord{ui.Super | ui.Alt, ui.KeyN}, true},
		{"darwin", chord{ui.Ctrl | ui.Alt, ui.KeyN}, true},
		{"windows", chord{ui.Ctrl | ui.Alt, ui.KeyN}, false},
		{"windows", chord{ui.Ctrl | ui.Alt | ui.Shift, ui.KeyQ}, false},
		{"windows", chord{ui.Ctrl | ui.Alt, ui.KeyF5}, true},
		{"windows", chord{ui.Alt, ui.KeyS}, true},
		{"windows", chord{ui.Ctrl, ui.KeyP}, true},
		{"linux", chord{ui.Ctrl | ui.Alt, ui.KeyN}, true},
		{"linux", chord{ui.Alt, ui.KeyS}, true},
	} {
		if got := c.k.textSafeOn(c.goos); got != c.safe {
			t.Errorf("%s %s: safe %v", c.goos, c.k.String(), got)
		}
	}
}

// No chord the app ships with types a character on the system it ships
// on, or it would never run in a text field nor be the menu's: on
// Windows New Branch is off Control and Alt, AltGr there. Nor does one
// take another's chord or one the menu has for itself.
func TestDefaultChordsTypeNothing(t *testing.T) {
	// What the menu takes on each system, of the roles main.go puts in
	// it, but those a default shares on purpose (Undo, Reload and Close
	// are the window's Undo, Refresh and Close Tab first).
	menu := map[string][]string{
		"darwin":  {"Super+Shift+W", "Super+X", "Super+C", "Super+V", "Super+A", "Super+Q", "Super+H", "Super+Alt+H", "Super+M", "Ctrl+Super+F", "Super+0", "Super+-", "Super+="},
		"windows": {"Ctrl+Shift+W", "Ctrl+X", "Ctrl+C", "Ctrl+V", "Ctrl+A", "Ctrl+Y", "Ctrl+Shift+R", "Ctrl+Shift+I", "Ctrl+M", "Ctrl+0", "Ctrl+-", "Ctrl+=", "F11"},
		"linux":   {"Ctrl+Shift+W", "Ctrl+X", "Ctrl+C", "Ctrl+V", "Ctrl+A", "Ctrl+Q", "Ctrl+Shift+R", "Ctrl+Shift+I", "Ctrl+M", "Ctrl+0", "Ctrl+-", "Ctrl+=", "F11"},
	}
	for _, goos := range []string{"darwin", "windows", "linux"} {
		// CmdOrCtrl as that system reads it.
		cmd := "Ctrl"
		if goos == "darwin" {
			cmd = "Super"
		}
		read := func(s string) chord {
			k, err := parseChord(strings.ReplaceAll(s, "CmdOrCtrl", cmd))
			if err != nil {
				t.Fatalf("%s: %v", s, err)
			}
			return k
		}
		taken := map[chord]string{}
		for _, s := range menu[goos] {
			taken[read(s)] = "the menu"
		}
		for id, keys := range defaultKeysOn(goos) {
			for _, s := range keys {
				k := read(s)
				if k.typesOn(goos) {
					t.Errorf("%s: %s's %s types a character", goos, id, s)
				}
				if by, ok := taken[k]; ok {
					t.Errorf("%s: %s's %s is %s's", goos, id, s, by)
				}
				taken[k] = id
			}
		}
	}
	if got := defaultKeysOn("windows")["new-branch"]; !slices.Equal(got, []string{"Ctrl+N"}) {
		t.Errorf("New Branch on Windows: %q", got)
	}
	for _, goos := range []string{"darwin", "linux"} {
		if got := defaultKeysOn(goos)["new-branch"]; !slices.Equal(got, []string{"CmdOrCtrl+Alt+N"}) {
			t.Errorf("New Branch on %s: %q", goos, got)
		}
	}
	// On this system, the menu has it, and the user's own still wins.
	if Accelerator(nil, "new-branch") == "" {
		t.Error("New Branch has no accelerator")
	}
	if got := Accelerator(map[string][]string{"new-branch": {"CmdOrCtrl+Shift+E"}}, "new-branch"); got != "CmdOrCtrl+Shift+E" {
		t.Errorf("the user's New Branch: %q", got)
	}
}

// A shortcut held with Option on macOS, or with what may be AltGr on
// Windows, does not run its command while the focus takes text: it
// types a character there. On macOS the field takes Option itself, and
// the app menu, which takes its keys before the window, is given no such
// chord.
func TestShortcutTypesCharacterInField(t *testing.T) {
	if runtime.GOOS == "darwin" {
		if got := Accelerator(map[string][]string{"show-history": {"Alt+S"}}, "show-history"); got != "" {
			t.Errorf("the menu takes Option-S: %q", got)
		}
	}
	var k chord
	switch runtime.GOOS {
	case "darwin":
		k = chord{ui.Alt, ui.KeyS}
	case "windows":
		k = chord{ui.Ctrl | ui.Alt, ui.KeyS}
	default:
		t.Skip("no chord types a character here")
	}
	h := newHarness(t)
	r := h.repoWith()
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()
	h.app.Settings.Keys = map[string][]string{"show-history": {k.String()}}
	h.click("Statistics")
	h.until("the statistics", h.shows("From"))
	if err := h.tt.Click("From"); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()
	h.tt.Key(k.mods, k.key)
	h.tt.Frame()
	h.tt.Frame()
	if tb.sel != "stats" {
		t.Fatalf("typing in a field ran the shortcut: %q", tb.sel)
	}
	// Outside a field it runs.
	h.click("Changes")
	h.tt.Frame()
	h.tt.Key(k.mods, k.key)
	h.until("the history", func() bool { return tb.sel == "history" })
}

func TestKeymap(t *testing.T) {
	km := newKeymap(map[string][]string{
		"refresh":      {},              // unbound
		"push":         {"CmdOrCtrl+R"}, // takes Refresh's chord
		"show-history": {"CmdOrCtrl+Shift+H", "nope+H"},
		"action:Lint":  {"CmdOrCtrl+Alt+L"},
	})
	cmdR := chord{ui.Cmd, ui.KeyR}
	if km.owner(cmdR) != "push" || len(km.byID["refresh"]) != 0 {
		t.Fatalf("Cmd+R runs %q; refresh has %v", km.owner(cmdR), km.byID["refresh"])
	}
	if km.owner(chord{ui.Cmd | ui.Shift, ui.KeyH}) != "show-history" || km.owner(chord{ui.Cmd | ui.Alt, ui.KeyL}) != "action:Lint" {
		t.Fatal("user bindings missing")
	}
	if len(km.errs) != 1 {
		t.Fatalf("errors %q", km.errs)
	}
	// The defaults the user left alone stay.
	if km.owner(chord{ui.Cmd, ui.KeyO}) != "open" || len(km.byID["palette"]) != 2 {
		t.Fatalf("defaults %v", km.byID)
	}
	// The menu, which takes its keys before a text field, is given no
	// chord of a field's own: typing an S stays typing.
	if got := Accelerator(map[string][]string{"stash": {"S", "CmdOrCtrl+Shift+S"}}, "stash"); got != "CmdOrCtrl+Shift+S" {
		t.Fatalf("stash's accelerator %q", got)
	}
	if got := Accelerator(nil, "stage-all"); got != "" {
		t.Fatalf("Select All's chord given to the menu: %q", got)
	}
}

func TestShortcutsRebound(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith("a.txt")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("a.txt"))
	tb := h.app.current()
	h.app.Settings.Keys = map[string][]string{"show-history": {"CmdOrCtrl+Shift+H"}, "palette": {}}
	changed := 0
	h.app.KeysChanged = func() { changed++ }
	h.tt.Frame()
	h.tt.Key(ui.Cmd|ui.Shift, ui.KeyH)
	h.until("the history", func() bool { return tb.sel == "history" })
	if changed != 1 || h.app.Accelerator("show-history") != "CmdOrCtrl+Shift+H" || h.app.Accelerator("palette") != "" {
		t.Fatalf("menu told %d times; accelerators %q %q", changed, h.app.Accelerator("show-history"), h.app.Accelerator("palette"))
	}
	// Unbound: the palette no longer opens on its old keys.
	h.tt.Key(ui.Cmd|ui.Shift, ui.KeyP)
	h.tt.Frame()
	if h.app.palette.open {
		t.Fatal("palette opened on an unbound chord")
	}
	// A write's shortcut: Stage All, from the Changes view's model.
	h.app.Settings.Keys["stage-all"] = []string{"CmdOrCtrl+Alt+S"}
	tb.sel = "changes"
	h.until("the model", func() bool { h.tt.Frame(); return tb.cv.model != nil && len(tb.cv.model.files[secUnstaged]) > 0 })
	h.tt.Key(ui.Cmd|ui.Alt, ui.KeyS)
	h.until("a.txt staged", func() bool { return r.Git("diff", "--cached", "--name-only") == "a.txt\n" })
	// The palette lists the shortcut beside its command.
	h.app.Settings.Keys["palette"] = nil
	delete(h.app.Settings.Keys, "palette")
	h.tt.Key(ui.Cmd|ui.Shift, ui.KeyP)
	h.tt.Frame()
	h.tt.Type("show hist")
	h.until("the shortcut in the palette", h.shows(chord{ui.Cmd | ui.Shift, ui.KeyH}.label()))
}

func TestKeysDialog(t *testing.T) {
	h := newHarness(t)
	h.app.openKeys()
	h.tt.Frame()
	h.tt.Type("show reflog")
	h.tt.Frame()
	ks := &h.app.keysUI
	// Record a shortcut for Show Reflog.
	h.click("Add Shortcut")
	if ks.recording != "show-reflog" {
		t.Fatalf("recording %q", ks.recording)
	}
	h.tt.Frame()
	h.tt.Key(ui.Cmd|ui.Shift, ui.KeyJ)
	h.tt.Frame()
	if got := h.app.Settings.Keys["show-reflog"]; !slices.Equal(got, []string{"CmdOrCtrl+Shift+J"}) || ks.recording != "" {
		t.Fatalf("keys %v, recording %q", h.app.Settings.Keys, ks.recording)
	}
	label := chord{ui.Cmd | ui.Shift, ui.KeyJ}.label()
	h.until("the new shortcut", h.shows(label))
	// Escape gives up, and the dialog stays.
	h.click(label)
	h.tt.Frame()
	h.tt.Key(0, ui.KeyEscape)
	h.tt.Frame()
	if !ks.open || ks.recording != "" || !slices.Equal(h.app.Settings.Keys["show-reflog"], []string{"CmdOrCtrl+Shift+J"}) {
		t.Fatalf("escape: open %v, recording %q, keys %v", ks.open, ks.recording, h.app.Settings.Keys)
	}
	// Refresh's chord moves to Show Reflog, and Refresh is told.
	h.click(label)
	h.tt.Frame()
	h.tt.Key(ui.Cmd, ui.KeyR)
	h.tt.Frame()
	if k := h.app.keys(); k.owner(chord{ui.Cmd, ui.KeyR}) != "show-reflog" || len(k.byID["refresh"]) != 0 {
		t.Fatalf("Cmd+R runs %q; refresh %v", k.owner(chord{ui.Cmd, ui.KeyR}), k.byID["refresh"])
	}
	h.until("the note", h.shows(chord{ui.Cmd, ui.KeyR}.label()+" no longer runs Refresh"))
	// Reset brings back the default, and nothing else changed is kept.
	h.click("Reset")
	h.tt.Frame()
	if _, ok := h.app.Settings.Keys["show-reflog"]; ok {
		t.Fatalf("keys %v", h.app.Settings.Keys)
	}
	ks.filter = "refresh"
	h.tt.Frame()
	h.click("Reset")
	h.tt.Frame()
	if h.app.Settings.Keys != nil || h.app.keys().owner(chord{ui.Cmd, ui.KeyR}) != "refresh" {
		t.Fatalf("keys %v", h.app.Settings.Keys)
	}
}

// A shortcut of an item of the app menu that has a role, as mygo makes
// them on Windows and Linux, where the window takes a key before those
// items: binding a command to one takes the key from the item, and the
// dialog says so, as it does for another command's.
func TestRoleKeyTakenIsSaid(t *testing.T) {
	h := newHarness(t)
	h.app.RoleKeys = func() map[string]string {
		return map[string]string{"CmdOrCtrl+M": "Minimize", "CmdOrCtrl+W": "Close Window", "CmdOrCtrl+Plus": "Zoom In", "F11": "Toggle Full Screen"}
	}
	h.app.openKeys()
	h.tt.Frame()
	h.tt.Type("show reflog")
	h.tt.Frame()
	ks := &h.app.keysUI
	label := locales.KeysAddShortcut()
	for _, c := range []struct {
		key  chord
		item string
	}{
		{chord{ui.Cmd, ui.KeyM}, "Minimize"},
		{chord{ui.Cmd, ui.KeyEqual}, "Zoom In"},
		{chord{0, ui.KeyF11}, "Toggle Full Screen"},
		// A command's key is told as the command's.
		{chord{ui.Cmd, ui.KeyW}, CommandName("close-tab")},
	} {
		h.click(label)
		h.tt.Frame()
		h.tt.Key(c.key.mods, c.key.key)
		h.tt.Frame()
		if got := h.app.Settings.Keys["show-reflog"]; !slices.Equal(got, []string{c.key.String()}) || ks.recording != "" {
			t.Fatalf("keys %v, recording %q", h.app.Settings.Keys, ks.recording)
		}
		h.until("the note", h.shows(locales.KeysNoLongerRuns(c.key.label(), c.item)))
		label = c.key.label()
	}
	// Where no item has a role, nothing is said.
	h.app.RoleKeys = nil
	h.click(label)
	h.tt.Frame()
	h.tt.Key(ui.Cmd, ui.KeyM)
	h.tt.Frame()
	if ks.note != "" {
		t.Fatalf("note %q", ks.note)
	}
}
