package repo

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
)

// oracle asks git, run in dir, where the repository is.
func oracle(t *testing.T, dir string) Location {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--path-format=absolute", "--absolute-git-dir", "--git-common-dir", "--is-bare-repository", "--show-toplevel")
	cmd.Dir = dir
	cmd.Env = gittest.Env()
	out, _ := cmd.Output()
	f := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(f) < 3 {
		t.Fatalf("git rev-parse in %s: %q", dir, out)
	}
	loc := Location{GitDir: resolve(f[0]), CommonDir: resolve(f[1]), Bare: f[2] == "true"}
	if len(f) > 3 {
		loc.WorkTree = resolve(f[3])
	}
	loc.Linked = loc.GitDir != loc.CommonDir
	return loc
}

func check(t *testing.T, dir string, submodule bool) {
	t.Helper()
	got, err := Discover(dir)
	if err != nil {
		t.Fatalf("%s: %v", dir, err)
	}
	want := oracle(t, dir)
	want.Submodule = submodule
	if got != want {
		t.Errorf("%s:\n got %+v\nwant %+v", dir, got, want)
	}
}

func TestDiscover(t *testing.T) {
	r := gittest.Init(t)
	r.Write("sub/dir/f", "x")
	r.Commit("one")
	check(t, r.Dir, false)
	check(t, filepath.Join(r.Dir, "sub", "dir"), false)
	// Inside a .git directory git sees no work tree; Ramus opening that
	// folder means its repository, work tree included.
	if got, err := Discover(filepath.Join(r.Dir, ".git")); err != nil || got.WorkTree != resolve(r.Dir) || got.Bare {
		t.Errorf("inside .git: %+v %v", got, err)
	}

	t.Run("bare", func(t *testing.T) {
		bare := filepath.Join(t.TempDir(), "b.git")
		r.Git("clone", "-q", "--bare", r.Dir, bare)
		check(t, bare, false)
		check(t, filepath.Join(bare, "refs"), false)
	})
	t.Run("linked worktree", func(t *testing.T) {
		wt := filepath.Join(t.TempDir(), "wt")
		r.Git("worktree", "add", "-q", "-b", "side", wt)
		check(t, wt, false)
		check(t, filepath.Join(wt, "sub"), false)
	})
	t.Run("separate git dir", func(t *testing.T) {
		base := t.TempDir()
		wt, gd := filepath.Join(base, "wt"), filepath.Join(base, "gd")
		cmd := exec.Command("git", "init", "-q", "--separate-git-dir", gd, wt)
		cmd.Env = gittest.Env()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
		check(t, wt, false)
	})
	t.Run("submodule", func(t *testing.T) {
		super := gittest.Init(t)
		super.Write("a", "1")
		super.Commit("super")
		super.Git("-c", "protocol.file.allow=always", "submodule", "add", "-q", r.Dir, "mod")
		check(t, filepath.Join(super.Dir, "mod"), true)
		check(t, filepath.Join(super.Dir, "mod", "sub"), true)
	})
	t.Run("symlink HEAD", func(t *testing.T) {
		s := gittest.Init(t)
		s.Commit("x")
		head := filepath.Join(s.GitDir(), "HEAD")
		os.Remove(head)
		if err := os.Symlink("refs/heads/main", head); err != nil {
			t.Skip(err)
		}
		check(t, s.Dir, false)
	})
	t.Run("worktree config", func(t *testing.T) {
		s := gittest.Init(t)
		s.Git("config", "extensions.worktreeConfig", "true")
		s.Git("config", "--worktree", "core.sparseCheckout", "false")
		check(t, s.Dir, false)
	})
}

func TestNotFound(t *testing.T) {
	dir := t.TempDir()
	if _, err := Discover(dir); !errors.Is(err, ErrNotFound) {
		// A temp dir inside some repository would make this meaningless.
		if _, gerr := exec.Command("git", "-C", dir, "rev-parse").Output(); gerr == nil {
			t.Skip("temp dir is inside a repository")
		}
		t.Fatalf("got %v", err)
	}
	// A .git that is not a repository is passed over, as git does.
	os.MkdirAll(filepath.Join(dir, "x", ".git"), 0o755)
	if _, err := Discover(filepath.Join(dir, "x")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty .git: %v", err)
	}
}

func TestBrokenGitfile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: nowhere\n"), 0o644)
	if _, err := Discover(dir); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}
