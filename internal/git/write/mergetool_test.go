package write

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveMerged(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(p, []byte("<<<<<<<\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The mode as the platform keeps it: Windows has no execute bit.
	before, _ := os.Stat(p)
	seen := ObserveMerged(p)
	if err := SaveMerged(p, []byte("done\n"), seen); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	st, _ := os.Stat(p)
	if string(data) != "done\n" || st.Mode().Perm() != before.Mode().Perm() {
		t.Fatalf("%q %v", data, st.Mode())
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(p, later, later); err != nil {
		t.Fatal(err)
	}
	var stale *StaleError
	if err := SaveMerged(p, []byte("again\n"), seen); !errors.As(err, &stale) {
		t.Fatalf("a changed file was overwritten: %v", err)
	}
}
