package tools

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

// The tests build commands and never start one: opening an app would take
// the user's focus.

func TestCommand(t *testing.T) {
	exe, _ := os.Executable()
	tool, err := Custom("mine", Editor, []string{exe, "--wait"})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "-rf")
	cmd, err := tool.Command(target, 0)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{exe, "--wait", target}; !slices.Equal(cmd.Args, want) {
		t.Fatalf("args %q, want %q", cmd.Args, want)
	}
	if _, err := tool.Command("-rf", 0); err == nil {
		t.Fatal("relative target accepted")
	}
	if _, err := Custom("bad", Editor, []string{"code"}); !errors.Is(err, ErrNotAbsolute) {
		t.Fatalf("program by name: %v", err)
	}
	if _, err := Custom("gone", Editor, []string{filepath.Join(t.TempDir(), "nope")}); err == nil {
		t.Fatal("missing program accepted")
	}
	term := Tool{Name: "t", Kind: Terminal, Argv: []string{exe}}
	dir := t.TempDir()
	if cmd, _ := term.Command(dir, 0); cmd.Dir != dir {
		t.Fatalf("terminal runs in %q", cmd.Dir)
	}
}

func TestDetect(t *testing.T) {
	tools := Detect()
	for _, tl := range tools {
		if !filepath.IsAbs(tl.Argv[0]) {
			t.Errorf("%s found at %q", tl.Name, tl.Argv[0])
		}
	}
	if runtime.GOOS == "darwin" && !slices.ContainsFunc(tools, func(t Tool) bool { return t.Name == "Terminal" && t.Kind == Terminal }) {
		t.Errorf("Terminal.app not found: %+v", tools)
	}
	t.Logf("found %d tools", len(tools))
}
