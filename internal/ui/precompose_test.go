package ui

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// On macOS a name the file system hands over decomposed (é as e and a
// combining accent) is the name git, with core.precomposeUnicode, lists
// composed: the app stages it by git's name, and finds its history from
// the decomposed path a terminal or Finder gives.
func TestPrecomposedNames(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("core.precomposeUnicode is for macOS")
	}
	h := newHarness(t)
	r := h.repoWith()
	if v := strings.TrimSpace(r.Git("config", "--get", "core.precomposeUnicode")); v != "true" {
		t.Skipf("core.precomposeUnicode is %q", v)
	}
	nfd, nfc := "café.txt", "café.txt"
	if err := os.WriteFile(filepath.Join(r.Dir, nfd), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the change", func() bool { return hasExact(h, nfc) })
	h.click("Stage All")
	h.until("staged", func() bool { return strings.Contains(r.Git("diff", "--cached", "--name-only", "-z"), nfc) })
	r.Commit("add café")

	h.app.ShowFile(filepath.Join(r.Dir, nfd), false)
	tb := h.app.current()
	h.until("its history", func() bool { return hasExact(h, "add café") })
	if tb.file.path != nfd && tb.file.path != nfc {
		t.Fatalf("file view of %q", tb.file.path)
	}
}
