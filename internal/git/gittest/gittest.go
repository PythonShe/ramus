// Package gittest builds fixture repositories with the git executable and
// runs git as the oracle that the engine's readers are compared with.
//
// Every git it runs ignores the user's and the system's configuration, so
// tests behave the same on every machine.
package gittest

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Repo is a fixture repository in a temporary directory.
type Repo struct {
	t   testing.TB
	Dir string
	// Env is added to every git call, after the defaults.
	Env []string
	// clock is the next commit's time, in seconds since the epoch.
	clock int64
}

// Env returns the environment that isolates git from the machine's
// configuration.
func Env() []string {
	env := []string{
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_AUTHOR_NAME=Ramus Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Ramus Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C",
		"TZ=UTC",
	}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "GIT_") || k == "LC_ALL" || k == "TZ" {
			continue
		}
		env = append(env, kv)
	}
	return env
}

// Portable reports whether a file at path, slash-separated, can be made
// on this system under that name. Windows refuses control characters and
// <>:"|?* in a name, drops a trailing space or dot, and reads a backslash
// as a separator.
func Portable(path string) bool {
	if runtime.GOOS != "windows" {
		return true
	}
	for name := range strings.SplitSeq(path, "/") {
		if strings.ContainsFunc(name, func(r rune) bool { return r < 32 || strings.ContainsRune(`<>:"|?*\\`, r) }) ||
			strings.HasSuffix(name, " ") || strings.HasSuffix(name, ".") {
			return false
		}
	}
	return true
}

// PortableNames is names without those Portable refuses.
func PortableNames(names ...string) []string {
	var out []string
	for _, n := range names {
		if Portable(n) {
			out = append(out, n)
		}
	}
	return out
}

// Shell is the full path of a POSIX shell, as an action names its
// program; on Windows the one that comes with git, when it is on PATH.
func Shell(t testing.TB) string {
	if runtime.GOOS != "windows" {
		return "/bin/sh"
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh:", err)
	}
	return sh
}

// Init creates an empty repository. Extra arguments go to git init.
func Init(t testing.TB, args ...string) *Repo {
	t.Helper()
	requireGit(t)
	dir := t.TempDir()
	// Resolve symlinks (macOS's /var is one) so paths compare with git's.
	if d, err := filepath.EvalSymlinks(dir); err == nil {
		dir = d
	}
	r := &Repo{t: t, Dir: dir, clock: 1_700_000_000}
	r.Git(append([]string{"init", "-q", "-b", "main"}, args...)...)
	return r
}

// Open wraps an existing repository, such as one under the fixtures
// directory, for running git in it.
func Open(t testing.TB, dir string) *Repo {
	t.Helper()
	requireGit(t)
	return &Repo{t: t, Dir: dir}
}

func requireGit(t testing.TB) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

// Git runs git in the repository and returns its standard output. It fails
// the test if git fails.
func (r *Repo) Git(args ...string) string {
	r.t.Helper()
	out, err := r.Run(nil, args...)
	if err != nil {
		r.t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return out
}

// Run runs git with stdin and returns its output, or an error carrying its
// standard error.
func (r *Repo) Run(stdin []byte, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", r.Dir}, args...)...)
	cmd.Env = append(Env(), r.Env...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// Write writes a file in the work tree, creating its directories.
func (r *Repo) Write(path, content string) {
	r.t.Helper()
	full := filepath.Join(r.Dir, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// Commit stages everything and commits it at the next tick of the
// repository's clock, returning the new commit's id.
func (r *Repo) Commit(msg string) string {
	r.t.Helper()
	r.clock += 60
	return r.CommitAt(msg, r.clock)
}

// Stage puts a file into the index without writing it to the work tree,
// for paths the file system refuses, such as names that are not UTF-8 on
// APFS.
func (r *Repo) Stage(path, content string) {
	r.t.Helper()
	blob, err := r.Run([]byte(content), "hash-object", "-w", "--stdin")
	if err != nil {
		r.t.Fatal(err)
	}
	line := "100644 " + strings.TrimSpace(blob) + "\t" + path + "\x00"
	if _, err := r.Run([]byte(line), "update-index", "-z", "--index-info"); err != nil {
		r.t.Fatal(err)
	}
}

// CommitIndex commits the index as it is, without staging the work tree.
func (r *Repo) CommitIndex(msg string) string {
	r.t.Helper()
	r.clock += 60
	return r.commit(msg, r.clock)
}

// CommitAt commits everything with the given author and committer time.
func (r *Repo) CommitAt(msg string, when int64) string {
	r.t.Helper()
	r.Git("add", "-A")
	return r.commit(msg, when)
}

func (r *Repo) commit(msg string, when int64) string {
	r.t.Helper()
	date := fmt.Sprintf("@%d +0000", when)
	cmd := []string{"-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", msg}
	old := r.Env
	r.Env = append(append([]string{}, old...), "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	defer func() { r.Env = old }()
	r.Git(cmd...)
	return strings.TrimSpace(r.Git("rev-parse", "HEAD"))
}

// Tick returns the repository clock advanced by a minute, for commits made
// through Git directly.
func (r *Repo) Tick() int64 {
	r.clock += 60
	return r.clock
}

// GitDir returns the repository's git directory.
func (r *Repo) GitDir() string {
	r.t.Helper()
	// git writes forward slashes on Windows too; the engine's paths do not.
	return filepath.FromSlash(strings.TrimSpace(r.Git("rev-parse", "--absolute-git-dir")))
}

// FixturesDir returns the directory of large fixture repositories kept
// outside the source tree, from RAMUS_FIXTURES, or skips the test when it
// is not set or missing.
func FixturesDir(t testing.TB) string {
	t.Helper()
	d := os.Getenv("RAMUS_FIXTURES")
	if d == "" {
		t.Skip("RAMUS_FIXTURES not set")
	}
	if _, err := os.Stat(d); err != nil {
		t.Skipf("RAMUS_FIXTURES: %v", err)
	}
	return d
}
