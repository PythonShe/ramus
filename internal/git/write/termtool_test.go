package write

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The terminal tools are git's: each tool git lists without needing a
// graphical session, and no other.
func TestTerminalToolsAreGits(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	cmd := exec.Command("git", "mergetool", "--tool-help")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANGUAGE=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	listed := 0
	for _, l := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(l, "\t") {
			continue
		}
		f := strings.Fields(l)
		if len(f) < 2 || f[1] != "Use" {
			continue
		}
		listed++
		name, gui := f[0], strings.Contains(l, "graphical session")
		if TerminalTool(name, "") == gui {
			t.Errorf("%s: terminal %v, git's help %q", name, !gui, l)
		}
	}
	if listed < 10 {
		t.Fatalf("read %d tools from %s", listed, out)
	}
	if TerminalTool("vimdiff", "my-merge $MERGED") {
		t.Error("a command of the user's own is refused")
	}
}
