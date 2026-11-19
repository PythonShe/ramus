package pathsafe

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCheck(t *testing.T) {
	for path, want := range map[string]Flag{
		"src/main.go":              0,
		"a/../b":                   DotDot,
		"./a":                      DotDot,
		".git/config":              DotGit,
		"sub/.GIT/hooks/x":         DotGit,
		".git. . /x":               DotGit,
		"GIT~1/config":             DotGit,
		".git::$INDEX_ALLOCATION":  DotGit,
		".g\u200cit/config":        DotGit,
		"\ufeff.GiT":               DotGit,
		".github/workflows/ci.yml": 0,
		".gitignore":               0,
		".gitx":                    0,
		"a\nb":                     LineBreak,
		"a\u2028b":                 LineBreak,
		"/etc/passwd":              Absolute,
		"C:/x":                     Absolute,
		"..\\x":                    DotDot,
	} {
		if got := check(path, false); got != want {
			t.Errorf("Check(%q) = %b, want %b", path, got, want)
		}
	}
}

// On Windows a device name or a colon in a component is flagged: the name
// would open a device or another file's data stream, not a file of its
// own.
func TestCheckOnWindows(t *testing.T) {
	for path, want := range map[string]Flag{
		"src/main.go":      0,
		"CON":              Reserved,
		"dir/nul.txt":      Reserved,
		"aux .c":           Reserved,
		"COM1":             Reserved,
		"lpt9.log":         Reserved,
		"COM\u00b2":        Reserved,
		"CONIN$":           Reserved,
		"a.txt:evil":       Reserved,
		"C:/x":             Absolute,
		"C:/x/b:c":         Absolute | Reserved,
		"console.txt":      0,
		"COM0":             0,
		"com10":            0,
		"connect/null.txt": 0,
	} {
		if got := check(path, true); got != want {
			t.Errorf("check(%q) = %b, want %b", path, got, want)
		}
	}
}

func TestInWorkTree(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	os.MkdirAll(filepath.Join(root, "dir"), 0o755)
	os.MkdirAll(filepath.Join(root, "real"), 0o755)
	if err := os.Symlink(outside, filepath.Join(root, "out")); err != nil {
		t.Skip("no symlinks here:", err)
	}
	os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "in"))
	os.Symlink(outside, filepath.Join(root, "dir", "leaf"))
	realRoot, _ := filepath.EvalSymlinks(root)
	for rel, ok := range map[string]bool{
		"dir/new.txt":   true,
		"new/deep/file": true,
		"in/file":       true,
		"dir/leaf":      true, // the link itself, not what it points to
		"out/file":      false,
		"../x":          false,
		".git/config":   false,
		"a\nb":          false,
		"":              false,
	} {
		p, err := InWorkTree(root, rel)
		if ok != (err == nil) {
			t.Errorf("%q: %v", rel, err)
			continue
		}
		if ok && !within(realRoot, p) {
			t.Errorf("%q resolved to %s outside the work tree", rel, p)
		}
		if !ok && rel != "" && rel != "out/file" && !errors.Is(err, ErrUnsafe) {
			t.Errorf("%q: %v", rel, err)
		}
	}
}

// A symlink in the work tree leading to its .git, or a submodule's, does
// not let a path reach a hook there.
func TestNotIntoTheGitDirectory(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".git", "hooks"), 0o755)
	os.MkdirAll(filepath.Join(root, "sub", ".git"), 0o755)
	os.WriteFile(filepath.Join(root, ".git", "hooks", "pre-commit"), []byte("x"), 0o755)
	if err := os.Symlink(".git", filepath.Join(root, "link")); err != nil {
		t.Skip(err)
	}
	os.Symlink(filepath.Join("sub", ".git"), filepath.Join(root, "sublink"))
	os.Symlink(filepath.Join(".git", "hooks", "pre-commit"), filepath.Join(root, "hook"))
	for _, rel := range []string{"link/hooks/pre-commit", "link/config", "sublink/config"} {
		if p, err := InWorkTree(root, rel); !errors.Is(err, ErrUnsafe) {
			t.Errorf("InWorkTree(%q) = %s, %v", rel, p, err)
		}
		if p, err := Target(root, rel); !errors.Is(err, ErrUnsafe) {
			t.Errorf("Target(%q) = %s, %v", rel, p, err)
		}
	}
	if p, err := Target(root, "hook"); !errors.Is(err, ErrUnsafe) {
		t.Errorf("Target(hook) = %s, %v", p, err)
	}
	// The link itself is the work tree's to remove.
	if _, err := InWorkTree(root, "link"); err != nil {
		t.Error(err)
	}
}

// A git directory in the work tree under another name than .git (git
// init --separate-git-dir into it, a linked worktree's kept there) is as
// closed to a symlink as .git is.
func TestNotIntoAGitDirectoryOfAnotherName(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"gitdata/hooks", "gitdata/objects", "gitdata/refs", "wtdata"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	os.WriteFile(filepath.Join(root, "gitdata", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644)
	os.WriteFile(filepath.Join(root, "gitdata", "hooks", "pre-commit"), []byte("x"), 0o755)
	os.WriteFile(filepath.Join(root, "wtdata", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644)
	os.WriteFile(filepath.Join(root, "wtdata", "commondir"), []byte("../gitdata\n"), 0o644)
	// Shaped like one in part only: a folder with a HEAD file.
	os.MkdirAll(filepath.Join(root, "docs"), 0o755)
	os.WriteFile(filepath.Join(root, "docs", "HEAD"), []byte("x"), 0o644)
	if err := os.Symlink("gitdata", filepath.Join(root, "link")); err != nil {
		t.Skip(err)
	}
	os.Symlink("wtdata", filepath.Join(root, "wtlink"))
	os.Symlink(filepath.Join("gitdata", "hooks", "pre-commit"), filepath.Join(root, "hook"))
	os.Symlink("docs", filepath.Join(root, "docslink"))
	for _, rel := range []string{"link/hooks/pre-commit", "link/config", "wtlink/HEAD"} {
		if p, err := InWorkTree(root, rel); !errors.Is(err, ErrUnsafe) {
			t.Errorf("InWorkTree(%q) = %s, %v", rel, p, err)
		}
	}
	if p, err := Target(root, "hook"); !errors.Is(err, ErrUnsafe) {
		t.Errorf("Target(hook) = %s, %v", p, err)
	}
	if _, err := InWorkTree(root, "docslink/HEAD"); err != nil {
		t.Errorf("a folder holding a HEAD file only: %v", err)
	}
}

func TestTarget(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(root, "file"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0o644)
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "away")); err != nil {
		t.Skip("no symlinks here:", err)
	}
	os.Symlink("file", filepath.Join(root, "near"))
	os.Symlink("missing", filepath.Join(root, "dangling"))
	for rel, ok := range map[string]bool{
		"file":     true,
		"near":     true,
		"absent":   true,
		"away":     false,
		"dangling": false,
		"../x":     false,
	} {
		if _, err := Target(root, rel); ok != (err == nil) {
			t.Errorf("%q: %v", rel, err)
		}
	}
}
