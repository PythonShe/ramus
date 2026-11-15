package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/gitexe"
	"ramus/internal/git/gittest"
	"ramus/internal/git/run"
)

// Run as git mergetool, the app resolves the conflicts of LOCAL and
// REMOTE over BASE into MERGED and says so once saved; cancelled, it
// leaves MERGED as it was.
func TestMergeTool(t *testing.T) {
	h := newHarness(t)
	h.tt = ui.NewTester(h.app.ToolView, 1100, 760)
	dir := t.TempDir()
	put := func(name, s string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	base := put("f_BASE_1.txt", "a\nb\nc\n")
	local := put("f_LOCAL_1.txt", "a\nB1\nc\n")
	remote := put("f_REMOTE_1.txt", "a\nB2\nc\n")
	merged := put("f.txt", "a\n<<<<<<< ours\nB1\n=======\nB2\n>>>>>>> theirs\nc\n")
	var got []bool
	h.app.MergeTool(base, local, remote, merged, func(ok bool) { got = append(got, ok) })
	h.until("the conflict", func() bool { return hasExact(h, "Conflict 1") && hasExact(h, "0 of 1 resolved") })
	if !h.tt.HasText("Ours: f_LOCAL_1.txt") || !h.tt.HasText("Base: f_BASE_1.txt") {
		t.Fatalf("labels: %q", h.tt.Texts())
	}
	h.click("Theirs")
	h.until("resolved", h.shows("1 of 1 resolved"))
	h.click("Save")
	h.until("saved", func() bool { return len(got) == 1 })
	data, _ := os.ReadFile(merged)
	if !got[0] || string(data) != "a\nB2\nc\n" {
		t.Fatalf("done %v, merged %q", got, data)
	}

	put("f.txt", "unchanged\n")
	h.app.MergeTool(base, local, remote, merged, func(ok bool) { got = append(got, ok) })
	h.until("the conflict again", h.shows("0 of 1 resolved"))
	h.click("Cancel")
	h.until("cancelled", func() bool { return len(got) == 2 })
	data, _ = os.ReadFile(merged)
	if got[1] || string(data) != "unchanged\n" {
		t.Fatalf("done %v, merged %q", got, data)
	}

	// A binary file keeps one side whole.
	bin := put("img_LOCAL.bin", "\x00local")
	h.app.MergeTool("", bin, remote, merged, func(ok bool) { got = append(got, ok) })
	h.until("the binary choice", h.shows("Keep Theirs"))
	h.click("Keep Ours")
	h.until("kept", func() bool { return len(got) == 3 })
	data, _ = os.ReadFile(merged)
	if !got[2] || string(data) != "\x00local" {
		t.Fatalf("done %v, merged %q", got, data)
	}
}

// The settings' switch makes the app git's difftool and mergetool in
// the global config, and takes it out again.
func TestDifftoolSwitch(t *testing.T) {
	g, err := gitexe.Find(context.Background(), os.Getenv("PATH"))
	if err != nil {
		t.Skip(err)
	}
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	env := []string{"GIT_CONFIG_GLOBAL=" + cfg}
	for _, kv := range gittest.Env() {
		if !strings.HasPrefix(kv, "GIT_CONFIG_GLOBAL=") {
			env = append(env, kv)
		}
	}
	h := newHarnessWith(t, run.New(g, env, 4))
	h.app.Program = "/Applications/Ramus.app/Contents/MacOS/ramus"
	set := func(on bool) {
		t.Helper()
		h.app.openSettings()
		h.until("read", func() bool { return h.app.settingsUI.difftoolRead })
		if h.app.settingsUI.difftool == on {
			t.Fatalf("already %v", on)
		}
		h.click("Use Ramus for git difftool and mergetool")
		// Each key is a git of its own: wait for the last.
		h.until("written", func() bool {
			b, _ := os.ReadFile(cfg)
			if on {
				return strings.Contains(string(b), "trustExitCode = true")
			}
			return !strings.Contains(string(b), "ramus") && !strings.Contains(string(b), "[merge]\n\ttool")
		})
		h.until("done", func() bool { return h.app.Runner.Running() == 0 })
	}
	set(true)
	set(false)
}
