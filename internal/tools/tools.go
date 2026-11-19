// Package tools opens repositories and files in the user's other apps:
// editors, IDEs, terminals and the file manager. Tools are found at
// absolute locations, never through the current directory, and paths are
// passed absolute, so a file named like an option is never read as one.
package tools

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
)

// Kind is what a tool opens.
type Kind uint8

const (
	Editor   Kind = iota // files and folders
	Terminal             // folders
)

// Tool is an installed app the app can open things in.
type Tool struct {
	Name string
	Kind Kind
	// Argv starts the tool, an absolute path first; the target is added
	// as the tool expects it.
	Argv []string
	// how adds the target to Argv.
	how func(argv []string, target string, line int) []string
}

// Command returns the command that opens target (absolute) in the tool;
// line, when positive, is a line to go to, for editors that take one.
func (t Tool) Command(target string, line int) (*exec.Cmd, error) {
	if !filepath.IsAbs(target) {
		return nil, fmt.Errorf("tools: %q is not an absolute path", target)
	}
	how := t.how
	if how == nil {
		how = appendTarget
	}
	argv := how(slices.Clone(t.Argv), target, line)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = userEnv()
	if t.Kind == Terminal {
		cmd.Dir = target
	}
	if err := Guard(cmd); err != nil {
		return nil, err
	}
	return cmd, nil
}

// Guard readies cmd, a program the user chose with arguments that may
// come from a repository (a file's or a branch's name), to start as
// asked: on Windows a batch file (.bat, .cmd) is run through cmd.exe
// with each argument quoted for it, and refused an argument that cannot
// be. Elsewhere it does nothing.
func Guard(cmd *exec.Cmd) error { return guard(cmd) }

// Program is what the tool runs: the app bundle of one opened through
// open(1), else its program.
func (t Tool) Program() string {
	if len(t.Argv) == 3 && t.Argv[0] == "/usr/bin/open" && t.Argv[1] == "-a" {
		return t.Argv[2]
	}
	if len(t.Argv) == 0 {
		return ""
	}
	return t.Argv[0]
}

func appendTarget(argv []string, target string, _ int) []string { return append(argv, target) }

// Open opens target in the tool and does not wait for the tool to quit.
func (t Tool) Open(target string, line int) error {
	cmd, err := t.Command(target, line)
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// ErrNotAbsolute is a custom tool whose program is not an absolute path:
// looked up by name, it could be any program on PATH, or in the
// repository itself.
var ErrNotAbsolute = errors.New("tools: give the program's full path")

// Custom makes a tool from the user's settings: argv's first word must be
// an absolute path to an executable file.
func Custom(name string, kind Kind, argv []string) (Tool, error) {
	if len(argv) == 0 || !filepath.IsAbs(argv[0]) {
		return Tool{}, ErrNotAbsolute
	}
	st, err := os.Stat(argv[0])
	if err != nil {
		return Tool{}, err
	}
	if st.IsDir() && !strings.HasSuffix(argv[0], ".app") {
		return Tool{}, fmt.Errorf("tools: %s is a folder", argv[0])
	}
	if strings.HasSuffix(argv[0], ".app") {
		return macApp(name, kind, argv[0]), nil
	}
	return Tool{Name: name, Kind: kind, Argv: slices.Clone(argv)}, nil
}

// environ is the user's environment, as their login shell sets it, once
// known (UseEnviron): the app's own, when started from the Finder or a
// desktop entry, lacks what the shell adds to PATH.
var environ atomic.Pointer[[]string]

// UseEnviron has tools found on the PATH of env, and started with env:
// the user's environment, which git runs with too.
func UseEnviron(env []string) {
	env = slices.Clone(env)
	environ.Store(&env)
}

// userEnv is the environment tools start with, nil for the app's own.
func userEnv() []string {
	if p := environ.Load(); p != nil {
		return *p
	}
	return nil
}

// lookPath finds a program on the user's PATH, refusing a match in a
// folder that is not absolute, the current one among them (Go returns
// exec.ErrDot for those).
func lookPath(name string) (string, bool) {
	path := os.Getenv("PATH")
	if env := userEnv(); env != nil {
		path = ""
		for _, kv := range env {
			if v, ok := strings.CutPrefix(kv, "PATH="); ok {
				path = v
			}
		}
	}
	for _, dir := range filepath.SplitList(path) {
		if !filepath.IsAbs(dir) {
			continue
		}
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0 {
			return p, true
		}
	}
	return "", false
}

// first returns the first of paths that exists.
func first(paths ...string) (string, bool) {
	for _, p := range paths {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
	}
	return "", false
}

var errNoOpener = errors.New("tools: no file manager to open with")

func dir(p string) string { return filepath.Dir(p) }
