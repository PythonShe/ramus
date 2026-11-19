package actions

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"testing"
)

// prog is a program by its full path, as an action names it.
var prog = map[bool]string{false: "/usr/bin/env", true: `C:\Windows\System32\cmd.exe`}[runtime.GOOS == "windows"]

func TestExpand(t *testing.T) {
	cmd := []string{prog, "--", "${repo}/x", "-m", "${prompt:Message}", "${file}", "${prompt:Message}${branch}", "${HOME}"}
	if got := Prompts(cmd); !slices.Equal(got, []string{"Message"}) {
		t.Fatalf("prompts %q", got)
	}
	v := Vars{Repo: "/r", Branch: "main", File: "/r/a b; rm -rf /"}
	got, err := Expand(cmd, v, map[string]string{"Message": "hi there"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{prog, "--", "/r/x", "-m", "hi there", "/r/a b; rm -rf /", "hi theremain", "${HOME}"}
	if !slices.Equal(got, want) {
		t.Fatalf("expanded %q, want %q", got, want)
	}
	if _, err := Expand([]string{prog, "${commit}"}, v, nil); !errors.Is(err, ErrMissing) {
		t.Fatalf("missing commit: %v", err)
	}
	if _, err := Expand([]string{prog, "${prompt:X}"}, v, nil); !errors.Is(err, ErrMissing) {
		t.Fatalf("missing answer: %v", err)
	}
	if _, err := Expand([]string{"echo"}, v, nil); err == nil {
		t.Fatal("program by name accepted")
	}
}

// The command as the log shows it keeps each question as written, never
// the answer typed to it.
func TestShownLeavesOutAnswers(t *testing.T) {
	cmd := []string{prog, "deploy", "--token=${prompt:Token}", "${branch}"}
	got := Shown(cmd, Vars{Branch: "main"})
	if want := prog + " deploy --token=${prompt:Token} main"; got != want {
		t.Fatalf("shown %q", got)
	}
}

func TestRun(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip(err)
	}
	var lines []string
	err = RunIn(context.Background(), []string{sh, "-c", "echo one; echo two >&2; pwd; exit 3"}, t.TempDir(), nil, func(l string) { lines = append(lines, l) })
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 || len(lines) != 3 || lines[0] != "one" || lines[1] != "two" {
		t.Fatalf("lines %q err %v", lines, err)
	}
	// The environment given is the action's, not the app's.
	lines = nil
	err = RunIn(context.Background(), []string{sh, "-c", "echo $RAMUS_MARK"}, t.TempDir(), append(os.Environ(), "RAMUS_MARK=from the shell"), func(l string) { lines = append(lines, l) })
	if err != nil || len(lines) != 1 || lines[0] != "from the shell" {
		t.Fatalf("environment: %q %v", lines, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := RunIn(ctx, []string{sh, "-c", "sleep 30"}, t.TempDir(), nil, func(string) {}); err == nil {
		t.Fatal("cancelled action ran")
	}
}
