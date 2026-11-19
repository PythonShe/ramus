package gitexe

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
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

// TooOldError is a git found that is older than Minimum, and no newer one.
type TooOldError struct {
	Path    string
	Version Version
}

func (e *TooOldError) Error() string {
	return fmt.Sprintf("gitexe: %s is git %s; Ramus needs %s or later", e.Path, e.Version, Minimum)
}

// Find looks for git on path (the user's PATH, resolved by the caller) and
// then in the places installers put it, and returns the first that answers
// git version and is Minimum or later; a *TooOldError when the only ones
// that answer are older. It never looks in the current directory or a
// work tree.
func Find(ctx context.Context, path string) (*Git, error) {
	return pick(ctx, candidates(path), sync.OnceValue(func() bool { return hasDevTools(ctx) }))
}

// pick probes cands in order; devTools is asked only on reaching macOS's
// shim, so a git found before it costs no xcode-select.
func pick(ctx context.Context, cands []string, devTools func() bool) (*Git, error) {
	var tried []string
	var old *TooOldError
	for _, p := range cands {
		if !filepath.IsAbs(p) {
			continue
		}
		if st, err := os.Stat(p); err != nil || st.IsDir() {
			continue
		}
		if runtime.GOOS == "darwin" && p == shim && !devTools() {
			continue
		}
		tried = append(tried, p)
		v, err := Probe(ctx, p)
		if err != nil {
			continue
		}
		if !v.AtLeast(Minimum.Major, Minimum.Minor, Minimum.Patch) {
			if old == nil {
				old = &TooOldError{Path: p, Version: v}
			}
			continue
		}
		return &Git{Path: p, Version: v}, nil
	}
	switch {
	case old != nil:
		return nil, old
	case len(tried) == 0:
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
// PATH or not, only when hasDevTools says they are there.
const shim = "/usr/bin/git"

func candidates(path string) []string {
	name := "git"
	if runtime.GOOS == "windows" {
		name = "git.exe"
	}
	var out []string
	for _, dir := range filepath.SplitList(path) {
		if filepath.IsAbs(dir) {
			out = append(out, filepath.Join(dir, name))
		}
	}
	switch runtime.GOOS {
	case "darwin":
		out = append(out, "/opt/homebrew/bin/git", "/usr/local/bin/git", "/opt/local/bin/git")
		if !slices.Contains(out, shim) {
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
