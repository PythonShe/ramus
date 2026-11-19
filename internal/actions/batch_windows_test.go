package actions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"ramus/internal/tools"
)

// An action that is a batch file, run on a branch whose name holds a
// command, gets the name as one argument: the command does not run.
func TestBatchActionGetsNoCommand(t *testing.T) {
	dir := t.TempDir()
	bat := filepath.Join(dir, "show.cmd")
	if err := os.WriteFile(bat, []byte("@echo off\r\necho ARG=%1\r\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	argv, err := Expand([]string{bat, "${branch}"}, Vars{Branch: "x&echo INJECTED"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	if err := Run(context.Background(), argv, dir, func(l string) { lines = append(lines, l) }); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || lines[0] != `ARG="x&echo INJECTED"` {
		t.Fatalf("output %q", lines)
	}
	if err := Run(context.Background(), []string{bat, "%PATH%"}, dir, func(string) {}); !errors.Is(err, tools.ErrBatchArgument) {
		t.Fatalf("an argument cmd.exe expands: %v", err)
	}
}
