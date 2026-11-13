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
	for _, p := range candidates(path) {
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
	out, err := cmd.Output()
	if err != nil {
		return Version{}, err
	}
	return ParseVersion(string(out))
}

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
		// /usr/bin/git is a shim that offers to install the developer tools
		// when they are missing; ask only when they are there.
		if _, err := os.Stat("/Library/Developer/CommandLineTools/usr/bin/git"); err == nil {
			out = append(out, "/usr/bin/git")
		} else if dev, err := exec.Command("/usr/bin/xcode-select", "-p").Output(); err == nil {
			if _, err := os.Stat(filepath.Join(strings.TrimSpace(string(dev)), "usr", "bin", "git")); err == nil {
				out = append(out, "/usr/bin/git")
			}
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
