package osfile

import (
	"errors"
	"testing"
)

// The bound holds for a file that says it is empty and is not, as the
// files of /proc do.
func TestReadFileLimitPastTheSizeStated(t *testing.T) {
	const path = "/proc/self/maps"
	b, err := ReadFile(path, 1<<20)
	if err != nil || len(b) < 2 {
		t.Fatalf("read %d bytes: %v", len(b), err)
	}
	if _, err := ReadFile(path, 1); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("past the limit: %v", err)
	}
}
