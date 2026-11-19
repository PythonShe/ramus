package pathsafe

import (
	"os"
	"path/filepath"
	"testing"
)

// Two spellings of one folder, where the file system ignores case, are
// one path, the one the disk holds; symlinks are resolved.
func TestRealSpellsAsOnDisk(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "MixedCase", "Sub")
	os.MkdirAll(dir, 0o755)
	os.Symlink(filepath.Join(base, "MixedCase"), filepath.Join(base, "link"))
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{dir, filepath.Join(base, "link", "Sub")} {
		if got, err := Real(p); err != nil || got != want {
			t.Errorf("Real(%s) = %s, %v; want %s", p, got, err, want)
		}
	}
	other := filepath.Join(base, "mixedcase", "SUB")
	if _, err := os.Stat(other); err != nil {
		t.Skip("the file system keeps case apart")
	}
	if got, err := Real(other); err != nil || got != want {
		t.Errorf("Real(%s) = %s, %v; want %s", other, got, err, want)
	}
	if _, err := Real(filepath.Join(base, "absent")); err == nil {
		t.Error("a path that is not there resolved")
	}
}
