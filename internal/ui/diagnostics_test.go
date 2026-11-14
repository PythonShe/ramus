package ui

import (
	"strings"
	"testing"

	"ramus/internal/crashlog"
	"ramus/internal/git/lifetime"
)

func TestDiagnostics(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith("a.txt")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("a.txt"))
	h.app.Crash = crashlog.New(t.TempDir(), "9.9.9")
	if _, err := h.app.Crash.Panic(r.Dir, r.GitDir(), lifetime.Panic{Value: "boom", Stack: "goroutine 1"}); err != nil {
		t.Fatal(err)
	}
	h.app.openDiagnostics()
	h.until("the crash report offered", func() bool { return h.app.diag.crashName != "" })
	text := h.app.diagnostics()
	for _, want := range []string{"Ramus: 9.9.9", "Git: ", "Open repositories: 1", "1: sha1; files refs"} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in\n%s", want, text)
		}
	}
	if strings.Contains(text, r.Dir) || strings.Contains(text, "boom") {
		t.Errorf("says more than asked:\n%s", text)
	}
	h.tt.Frame()
	h.click("Include the latest crash report (" + h.app.diag.crashName + ")")
	h.click("Copy Diagnostics")
	h.until("the copy", func() bool {
		got := h.tt.Clipboard()
		return strings.HasPrefix(got, "Ramus: 9.9.9") && strings.Contains(got, "Panic: boom")
	})
	if got := h.tt.Clipboard(); got != h.app.diagnostics() {
		t.Fatalf("copied text differs from the text shown")
	}
}
