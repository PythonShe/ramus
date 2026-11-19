package trust

import (
	"os"
	"path/filepath"
	"testing"
)

// A folder made again at a trusted path is another folder, though Linux
// gives it the inode of the one removed, as ext4 does at once.
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
