package trust

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings", "trust.json")
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(dir, "repo", ".git")
	os.MkdirAll(repo, 0o755)
	link := filepath.Join(dir, "link")
	os.Symlink(filepath.Join(dir, "repo"), link)
	if s.Trusted(repo) {
		t.Fatal("trusted before asked")
	}
	if err := s.Set(repo, true); err != nil {
		t.Fatal(err)
	}
	if !s.Trusted(filepath.Join(link, ".git")) {
		t.Error("the same repository through a symlink is not trusted")
	}
	again, err := Load(path)
	if err != nil || !again.Trusted(repo) {
		t.Fatalf("not saved: %v", err)
	}
	if err := again.Set(repo, false); err != nil {
		t.Fatal(err)
	}
	if third, _ := Load(path); third.Trusted(repo) {
		t.Error("still trusted after revoking")
	}
	os.WriteFile(path, []byte("{broken"), 0o600)
	if _, err := Load(path); err == nil {
		t.Error("a broken store loaded")
	}
}
