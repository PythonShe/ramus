//go:build !windows

package trust

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
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

// An identity adopted for an entry from before identities, whose save
// failed, is saved by the next Trusted, or by Wait at a quit, once the
// disk takes writes again: else a folder put at the path would be
// trusted at the next launch.
func TestAdoptionNotSavedIsSavedAgain(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions do not hold for root")
	}
	for _, by := range []string{"Trusted", "Wait"} {
		t.Run(by, func(t *testing.T) {
			state := t.TempDir()
			repo := filepath.Join(t.TempDir(), "repo")
			if err := os.Mkdir(repo, 0o755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(state, "trust.json")
			os.WriteFile(path, []byte(`{"trusted": [`+strconv.Quote(clean(repo))+`]}`), 0o600)
			s, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			// The state folder takes no new file: the save fails.
			os.Chmod(state, 0o500)
			t.Cleanup(func() { os.Chmod(state, 0o700) })
			if !s.Trusted(repo) {
				t.Fatal("the entry from before identities is not trusted")
			}
			if b, _ := os.ReadFile(path); unknown(b, "trusted") != nil {
				t.Fatalf("saved into a folder that takes no file: %s", b)
			}
			os.Chmod(state, 0o700)
			if by == "Trusted" {
				s.Trusted(repo)
			}
			if !s.Wait(10 * time.Second) {
				t.Fatal("not saved within 10s")
			}
			if b, _ := os.ReadFile(path); unknown(b, "trusted") == nil {
				t.Fatalf("the identity is not saved: %s", b)
			}

			// The next launch, another folder at the path.
			if err := os.Rename(repo, repo+".old"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(repo, 0o755); err != nil {
				t.Fatal(err)
			}
			again, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if again.Trusted(repo) {
				t.Error("the folder put at the path is trusted at the next launch")
			}
		})
	}
}
