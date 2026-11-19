//go:build !windows

package trust

import (
	"os"
	"path/filepath"
	"testing"
)

// A store file that can be neither read nor moved aside is left as it is:
// the store trusts nothing and saves nothing over it.
func TestUnreadableStoreThatCannotMove(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions do not hold for root")
	}
	dir := filepath.Join(t.TempDir(), "settings")
	os.Mkdir(dir, 0o755)
	path := filepath.Join(dir, "trust.json")
	os.WriteFile(path, []byte("{broken"), 0o600)
	os.Chmod(dir, 0o555)
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	s, err := Load(path)
	if err == nil || s == nil {
		t.Fatalf("%v %v", s, err)
	}
	repo := filepath.Join(t.TempDir(), ".git")
	os.MkdirAll(repo, 0o755)
	if err := s.Set(repo, true); err == nil {
		t.Error("saved over a file it could not read")
	}
	if b, _ := os.ReadFile(path); string(b) != "{broken" {
		t.Errorf("the file is now %q", b)
	}
}
