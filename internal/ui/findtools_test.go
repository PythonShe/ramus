package ui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ramus/internal/tools"
)

// The tools are looked for again only when the editor or the terminal
// changes, the last look's result is the one kept, and an editor the
// settings name that cannot be used is said, not replaced in silence.
func TestFindTools(t *testing.T) {
	h := newHarness(t)
	h.until("the tools", func() bool { return h.app.palette.found })
	gen := h.app.toolsGen
	h.app.openSettings()
	h.tt.Frame()
	h.app.settingsUI.emails = "me@example.com"
	h.tt.Frame()
	if len(h.app.Settings.Emails) != 1 || h.app.toolsGen != gen {
		t.Fatalf("emails %q, looked again: %v", h.app.Settings.Emails, h.app.toolsGen != gen)
	}
	h.app.settingsUI.open = false

	gone := filepath.Join(t.TempDir(), "gone-editor")
	exe, _ := os.Executable()
	h.app.Settings.Editor = []string{exe}
	h.app.findTools()
	h.app.Settings.Editor = []string{gone}
	h.app.findTools()
	h.until("the editor said", func() bool { return strings.Contains(h.app.notice, gone) })
	h.pumpFor(500e6)
	if slices.ContainsFunc(h.app.palette.tools, func(tl tools.Tool) bool { return tl.Name == yourEditor }) {
		t.Fatal("an older look's editor kept")
	}
	// Said once.
	h.app.notice = ""
	h.app.findTools()
	h.pumpFor(500e6)
	if h.app.notice != "" {
		t.Fatalf("said again: %q", h.app.notice)
	}
}
