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
		if got := Check(path); got != want {
			t.Errorf("Check(%q) = %b, want %b", path, got, want)
		}
	}
}

func TestInWorkTree(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	os.MkdirAll(filepath.Join(root, "dir"), 0o755)
	os.MkdirAll(filepath.Join(root, "real"), 0o755)
	os.Symlink(outside, filepath.Join(root, "out"))
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
