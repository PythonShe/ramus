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
	if err := RunIn(context.Background(), argv, dir, nil, func(l string) { lines = append(lines, l) }); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || lines[0] != `ARG="x&echo INJECTED"` {
		t.Fatalf("output %q", lines)
	}
	if err := RunIn(context.Background(), []string{bat, "%PATH%"}, dir, nil, func(string) {}); !errors.Is(err, tools.ErrBatchArgument) {
		t.Fatalf("an argument cmd.exe expands: %v", err)
	}
}

// A batch action that turns delayed expansion on would get !OS! as the
// value of OS: an argument holding ! is refused, and the script never
// runs.
func TestBatchActionGetsNoDelayedExpansion(t *testing.T) {
	dir := t.TempDir()
	bat := filepath.Join(dir, "show.cmd")
	marker := filepath.Join(dir, "ran")
	script := "@echo off\r\nsetlocal EnableDelayedExpansion\r\necho ran>\"" + marker + "\"\r\necho ARG=%1\r\n"
	if err := os.WriteFile(bat, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	argv, err := Expand([]string{bat, "${branch}"}, Vars{Branch: "wip-!OS!"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := RunIn(context.Background(), argv, dir, nil, func(string) {}); !errors.Is(err, tools.ErrBatchArgument) {
		t.Fatalf("an argument delayed expansion expands: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the script ran")
	}
}
