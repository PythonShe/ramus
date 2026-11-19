package gitexe

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Git is a git executable and its version.
type Git struct {
	Path    string
	Version Version
}

// ErrNotFound means no usable git was found.
var ErrNotFound = errors.New("gitexe: git not found")

// probeTimeout bounds git version; a git that hangs on it is not usable.
const probeTimeout = 5 * time.Second

// Find looks for git on path (the user's PATH, resolved by the caller) and
// then in the places installers put it, and returns the first that answers
// git version. It never looks in the current directory or a work tree.
func Find(ctx context.Context, path string) (*Git, error) {
	var tried []string
	devTools := sync.OnceValue(func() bool { return hasDevTools(ctx) })
	for _, p := range candidates(path, devTools) {
		if !filepath.IsAbs(p) {
			continue
		}
		if st, err := os.Stat(p); err != nil || st.IsDir() {
			continue
		}
		tried = append(tried, p)
		v, err := Probe(ctx, p)
		if err != nil {
			continue
		}
		return &Git{Path: p, Version: v}, nil
	}
	if len(tried) == 0 {
		return nil, ErrNotFound
	}
	return nil, fmt.Errorf("%w: none of %s answered git version", ErrNotFound, strings.Join(tried, ", "))
}

// Probe runs git version on the executable at p.
func Probe(ctx context.Context, p string) (Version, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, p, "version")
	cmd.Dir = os.TempDir()
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "LC_ALL=C")
	StopTree(cmd)
	Hide(cmd)
	// What a wrapper script started may hold the output past the limit.
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return Version{}, err
	}
	return ParseVersion(string(out))
}

// shim is macOS's /usr/bin/git, which offers to install the developer
// tools when they are missing, a dialog on every launch: it is tried, from
// PATH or not, only when devTools says they are there.
const shim = "/usr/bin/git"

func candidates(path string, devTools func() bool) []string {
	name := "git"
	if runtime.GOOS == "windows" {
		name = "git.exe"
	}
	var out []string
	for _, dir := range filepath.SplitList(path) {
		if filepath.IsAbs(dir) {
			p := filepath.Join(dir, name)
			if runtime.GOOS == "darwin" && p == shim && !devTools() {
				continue
			}
			out = append(out, p)
		}
	}
	switch runtime.GOOS {
	case "darwin":
		out = append(out, "/opt/homebrew/bin/git", "/usr/local/bin/git", "/opt/local/bin/git")
		if devTools() {
			out = append(out, shim)
		}
	case "windows":
		for _, env := range []string{"ProgramFiles", "ProgramW6432", "ProgramFiles(x86)", "LocalAppData"} {
			base := os.Getenv(env)
			if base == "" || !filepath.IsAbs(base) {
				continue
			}
			if env == "LocalAppData" {
				base = filepath.Join(base, "Programs")
			}
			out = append(out, filepath.Join(base, "Git", "cmd", "git.exe"))
		}
	default:
		out = append(out, "/usr/bin/git", "/usr/local/bin/git", "/bin/git")
	}
	return out
}

// hasDevTools reports whether macOS's developer tools, which hold the git
// behind its shim, are installed.
func hasDevTools(ctx context.Context) bool {
	if _, err := os.Stat("/Library/Developer/CommandLineTools/usr/bin/git"); err == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	dev, err := exec.CommandContext(ctx, "/usr/bin/xcode-select", "-p").Output()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(strings.TrimSpace(string(dev)), "usr", "bin", "git"))
	return err == nil
}
