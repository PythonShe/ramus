package tools

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A batch file set as the editor, as VS Code's code.cmd, gets a file
// named to run a command as one argument: the command does not run.
func TestBatchEditorGetsNoCommand(t *testing.T) {
	dir := t.TempDir()
	bat := filepath.Join(dir, "show.cmd")
	if err := os.WriteFile(bat, []byte("@echo off\r\necho ARG=%1\r\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "ran")
	tool, err := Custom("mine", Editor, []string{bat})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "x&echo ran>"+marker+"&.md")
	cmd, err := tool.Command(target, 0)
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("the name ran a command: %s", out)
	}
	if !strings.Contains(string(out), "ARG=\""+target+"\"") {
		t.Fatalf("argument arrived as %s", out)
	}
	if _, err := tool.Command(filepath.Join(dir, "%PATH%.md"), 0); !errors.Is(err, ErrBatchArgument) {
		t.Fatalf("a name cmd.exe expands: %v", err)
	}
	// A script that turns delayed expansion on would expand it.
	if _, err := tool.Command(filepath.Join(dir, "!OS!.md"), 0); !errors.Is(err, ErrBatchArgument) {
		t.Fatalf("a name delayed expansion expands: %v", err)
	}
}
