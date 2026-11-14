package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ramus/internal/git/gitexe"
	"ramus/internal/git/gittest"
	"ramus/internal/git/run"
)

// A submodule not checked out says so and is checked out when chosen;
// one checked out opens in a tab; its URL is synced from .gitmodules.
func TestSubmoduleActions(t *testing.T) {
	g, err := gitexe.Find(t.Context(), os.Getenv("PATH"))
	if err != nil {
		t.Skip(err)
	}
	// Local paths stand in for a remote; git refuses them for submodules
	// unless told.
	h := newHarnessWith(t, run.New(g, append(gittest.Env(),
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=protocol.file.allow", "GIT_CONFIG_VALUE_0=always"), 4))
	r := h.repoWith()
	sub := gittest.Init(t)
	sub.Write("s.txt", "1")
	sub.Commit("one")
	r.Git("-c", "protocol.file.allow=always", "submodule", "add", "-q", sub.Dir, "lib")
	r.Commit("with a submodule")
	r.Git("submodule", "deinit", "-q", "-f", "lib")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	tb := h.app.current()

	missing := "lib  not checked out"
	h.until("the submodule", h.shows(missing))
	h.click(missing)
	h.until("the offer", h.shows("Submodule lib is not checked out"))
	h.click("Check Out")
	h.until("checked out", func() bool {
		_, err := os.Stat(filepath.Join(r.Dir, "lib", "s.txt"))
		return err == nil && hasExact(h, "lib")
	})

	h.menu("lib", "Open in New Tab")
	h.until("its tab", func() bool { return len(h.app.tabs) == 2 })
	if h.app.current().s.Path != filepath.Join(r.Dir, "lib") {
		t.Fatalf("opened %s", h.app.current().s.Path)
	}
	h.app.Close(1)
	h.app.active = 0

	moved := filepath.Join(t.TempDir(), "moved")
	r.Git("config", "--file", ".gitmodules", "submodule.lib.url", moved)
	h.menu("lib", "Sync URL")
	h.until("synced", func() bool {
		out, err := r.Run(nil, "config", "submodule.lib.url")
		return err == nil && strings.TrimSpace(out) == moved
	})
	h.idle(tb, "the sync")

	h.menu("Submodules (1)", "Update All Submodules")
	h.until("the update", func() bool {
		l := tb.s.Log
		return len(l) > 0 && l[len(l)-1].Name == "Update every submodule" && !l[len(l)-1].Running
	})
}
