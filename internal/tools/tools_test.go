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

// Once the user's environment is known, tools are looked for on its
// PATH, never in a folder that is not absolute, and start with it.
func TestUserEnviron(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows finds tools at their installed places, not on PATH")
	}
	defer environ.Store(nil)
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "ramus-test-editor"), []byte("#!/bin/sh\n"), 0o755)
	if _, ok := lookPath("ramus-test-editor"); ok {
		t.Fatal("found before the user's PATH was known")
	}
	UseEnviron([]string{"HOME=/h", "PATH=relative:" + bin, "MARK=1"})
	if p, ok := lookPath("ramus-test-editor"); !ok || p != filepath.Join(bin, "ramus-test-editor") {
		t.Fatalf("on the user's PATH: %q %v", p, ok)
	}
	exe, _ := os.Executable()
	cmd, err := Tool{Name: "t", Argv: []string{exe}}.Command(t.TempDir(), 0)
	if err != nil || !slices.Contains(cmd.Env, "MARK=1") {
		t.Fatalf("started with %q (%v)", cmd.Env, err)
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
