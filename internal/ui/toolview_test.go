package ui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"

	"ramus/internal/cli"
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

// The settings offer the git config that makes the app git's tools, to
// copy; nothing writes it.
func TestToolConfigCopied(t *testing.T) {
	h := newHarness(t)
	h.app.Program = "/Applications/Ramus.app/Contents/MacOS/ramus"
	h.app.openSettings()
	h.until("the snippet", h.shows("mergetool \"ramus\""))
	h.click("Copy")
	h.until("copied", func() bool {
		return h.tt.Clipboard() == cli.ToolConfig(h.app.Program)
	})
}
