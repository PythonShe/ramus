package actions

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"testing"
)

func TestExpand(t *testing.T) {
	cmd := []string{"/usr/bin/env", "--", "${repo}/x", "-m", "${prompt:Message}", "${file}", "${prompt:Message}${branch}", "${HOME}"}
	if got := Prompts(cmd); !slices.Equal(got, []string{"Message"}) {
		t.Fatalf("prompts %q", got)
	}
	v := Vars{Repo: "/r", Branch: "main", File: "/r/a b; rm -rf /"}
	got, err := Expand(cmd, v, map[string]string{"Message": "hi there"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/usr/bin/env", "--", "/r/x", "-m", "hi there", "/r/a b; rm -rf /", "hi theremain", "${HOME}"}
	if !slices.Equal(got, want) {
		t.Fatalf("expanded %q, want %q", got, want)
	}
	if _, err := Expand([]string{"/bin/echo", "${commit}"}, v, nil); !errors.Is(err, ErrMissing) {
		t.Fatalf("missing commit: %v", err)
	}
	if _, err := Expand([]string{"/bin/echo", "${prompt:X}"}, v, nil); !errors.Is(err, ErrMissing) {
		t.Fatalf("missing answer: %v", err)
	}
	if _, err := Expand([]string{"echo"}, v, nil); err == nil {
		t.Fatal("program by name accepted")
	}
}

func TestRun(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip(err)
	}
	var lines []string
	err = Run(context.Background(), []string{sh, "-c", "echo one; echo two >&2; pwd; exit 3"}, t.TempDir(), func(l string) { lines = append(lines, l) })
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 || len(lines) != 3 || lines[0] != "one" || lines[1] != "two" {
		t.Fatalf("lines %q err %v", lines, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, []string{sh, "-c", "sleep 30"}, t.TempDir(), func(string) {}); err == nil {
		t.Fatal("cancelled action ran")
	}
}
