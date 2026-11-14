package ui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"ramus/internal/git/gitexe"
	"ramus/internal/git/gittest"
	"ramus/internal/git/run"
)

// A view left while it reads stops reading, and reads again when shown
// again; a tab put in the background stops too.
func TestLeftViewReadsAgain(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as git")
	}
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip(err)
	}
	// While the flag file exists, file history hangs.
	bin := t.TempDir()
	flag := filepath.Join(bin, "hang")
	script := "#!/bin/sh\nif [ -e " + flag + " ]; then for a in \"$@\"; do [ \"$a\" = --name-only ] && exec sleep 61; done; fi\nexec " + real + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(flag, nil, 0o644)
	g, err := gitexe.Find(context.Background(), bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t)
	runner := run.New(g, gittest.Env(), 8)
	h.app.Runner = runner
	r := h.repoWith()
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("open", h.shows("No changes"))
	// The repository's cat-file process counts as running throughout.
	h.settle()
	base := runner.Running()

	h.app.ShowFile(filepath.Join(r.Dir, "README"), false)
	h.until("the file history running", func() bool { return runner.Running() > base })
	h.click("Changes")
	h.until("the file history stopped", func() bool { return runner.Running() == base })

	os.Remove(flag)
	h.app.current().sel = "file"
	h.until("the file history read again", h.shows("one"))

	// A tab in the background stops what its view reads.
	os.WriteFile(flag, nil, 0o644)
	h.app.current().file.logKey = ""
	h.until("reading again", func() bool { return runner.Running() > base })
	first := h.app.current()
	other := h.repoWith()
	h.trust.Set(other.GitDir(), true)
	h.app.Open(other.Dir)
	h.until("the other tab", func() bool {
		// The hanging git is the only sleep 61 on the machine.
		err := exec.Command("pgrep", "-f", "sleep 61").Run()
		return h.app.current() != first && first.shown == "" && err != nil
	})
}
