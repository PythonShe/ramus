package ui

import (
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
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
		if !slices.ContainsFunc(bindable, func(b struct{ id, name string }) bool { return b.id == id }) {
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
	h.tt.Type("show history")
	h.tt.Frame()
	ks := &h.app.keysUI
	// Record a shortcut for Show History.
	h.click("None")
	if ks.recording != "show-history" {
		t.Fatalf("recording %q", ks.recording)
	}
	h.tt.Frame()
	h.tt.Key(ui.Cmd|ui.Shift, ui.KeyH)
	h.tt.Frame()
	if got := h.app.Settings.Keys["show-history"]; !slices.Equal(got, []string{"CmdOrCtrl+Shift+H"}) || ks.recording != "" {
		t.Fatalf("keys %v, recording %q", h.app.Settings.Keys, ks.recording)
	}
	label := chord{ui.Cmd | ui.Shift, ui.KeyH}.label()
	h.until("the new shortcut", h.shows(label))
	// Escape gives up, and the dialog stays.
	h.click(label)
	h.tt.Frame()
	h.tt.Key(0, ui.KeyEscape)
	h.tt.Frame()
	if !ks.open || ks.recording != "" || !slices.Equal(h.app.Settings.Keys["show-history"], []string{"CmdOrCtrl+Shift+H"}) {
		t.Fatalf("escape: open %v, recording %q, keys %v", ks.open, ks.recording, h.app.Settings.Keys)
	}
	// Refresh's chord moves to Show History, and Refresh is told.
	h.click(label)
	h.tt.Frame()
	h.tt.Key(ui.Cmd, ui.KeyR)
	h.tt.Frame()
	if k := h.app.keys(); k.owner(chord{ui.Cmd, ui.KeyR}) != "show-history" || len(k.byID["refresh"]) != 0 {
		t.Fatalf("Cmd+R runs %q; refresh %v", k.owner(chord{ui.Cmd, ui.KeyR}), k.byID["refresh"])
	}
	h.until("the note", h.shows(chord{ui.Cmd, ui.KeyR}.label()+" no longer runs Refresh"))
	// Reset brings back the default, and nothing else changed is kept.
	h.click("Reset")
	h.tt.Frame()
	if _, ok := h.app.Settings.Keys["show-history"]; ok {
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
