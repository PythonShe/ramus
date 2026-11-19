//go:build linux || darwin

package trust

import (
	"os"
	"path/filepath"
	"testing"
)

// A folder removed and made again at a trusted path is another folder,
// though ext4 gives it the inode of the one removed, as it does at once:
// they were not made at the same moment. That is what the identity
// guarantees where the file system keeps a birth time (APFS, HFS+, ext4,
// XFS, btrfs); where it keeps none the test skips, as the identity is
// then the device and inode alone, which such a folder can repeat. A
// volume mounted again is not the same folder (identity_unix.go).
func TestFolderMadeAgainIsAnother(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "project")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	made := birth(dir)
	if made == "" {
		t.Skip("this file system keeps no birth time")
	}
	first, err := identity(dir)
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(dir)
	// The file system's clock moves on, as it has by the time an archive
	// is extracted again: it is coarse, a few milliseconds a tick.
	probe := filepath.Join(parent, "probe")
	for {
		if err := os.Mkdir(probe, 0o755); err != nil {
			t.Fatal(err)
		}
		later := birth(probe)
		os.Remove(probe)
		if later != made {
			break
		}
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	again, err := identity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if again == first {
		t.Fatalf("the folder made again has the identity of the first, %s", first)
	}
}
