package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// One folder opened by two spellings, where the file system ignores case,
// is one repository of the library, keyed as the disk spells it.
func TestCleanFoldsCaseAsTheDisk(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Repo")
	os.Mkdir(dir, 0o755)
	other := strings.ToUpper(dir)
	if _, err := os.Stat(other); err != nil {
		t.Skip("the file system keeps case apart")
	}
	if a, b := Clean(dir), Clean(other); a != b || filepath.Base(a) != "Repo" {
		t.Fatalf("%s and %s", a, b)
	}
	l, _ := Load(filepath.Join(t.TempDir(), "library.json"))
	l.Opened(dir, time.Now())
	l.Opened(other, time.Now())
	if n := len(l.Recent(10)); n != 1 {
		t.Errorf("%d entries", n)
	}
}
