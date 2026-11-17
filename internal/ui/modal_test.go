package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// The app menu's commands wait while a dialog is open, as the window's
// own shortcuts do: Cmd+Enter in a dialog commits nothing staged behind
// it, and the Repository items say they cannot run.
func TestMenuCommandsWaitForDialogs(t *testing.T) {
	h := newHarness(t)
	var told []bool
	h.app.RepoCommandsChanged = func(on bool) { told = append(told, on) }
	r := h.repoWith()
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	r.Write("a.txt", "a")
	r.Git("add", "a.txt")
	h.until("staged", h.shows("Staged (1)"))
	tb := h.app.current()
	before := strings.TrimSpace(r.Git("rev-parse", "HEAD"))
	tb.commit.subject = "Add a"
	h.until("ready", func() bool { return commitReady(tb) })

	h.app.RunCommand("new-branch")
	h.until("the dialog", h.app.DialogOpen)
	if h.app.RepoCommandsOn() || len(told) != 2 || told[1] {
		t.Fatalf("the Repository items still act: told %v", told)
	}
	h.app.RunCommand("commit")
	for range 10 {
		h.tt.Frame()
		h.note()
	}
	if tb.commit.busy || strings.TrimSpace(r.Git("rev-parse", "HEAD")) != before {
		t.Fatal("the menu committed behind the dialog")
	}

	h.tt.Key(0, ui.KeyEscape)
	h.until("the dialog closed", func() bool { return !h.app.DialogOpen() })
	if !h.app.RepoCommandsOn() || len(told) != 3 || !told[2] {
		t.Fatalf("told %v", told)
	}
	h.app.RunCommand("commit")
	h.until("the commit", func() bool { return strings.TrimSpace(r.Git("rev-parse", "HEAD")) != before })
}

// Every dialog goes through modal or dialogBase, which DialogOpen counts.
func TestDialogsAreCounted(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") || name == "modal.go" {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, call := range []string{"ui.Modal(", "ui.DialogBase("} {
			if strings.Contains(string(src), call) {
				t.Errorf("%s calls %s: use a.modal or a.dialogBase, which the menu's commands wait for", name, call)
			}
		}
	}
}
