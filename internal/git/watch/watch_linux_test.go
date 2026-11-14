package watch

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// limitWatches makes inotify refuse watches past n, as at
// max_user_watches.
func limitWatches(t *testing.T, n int) {
	added := 0
	inotifyAddWatch = func(fd int, path string, mask uint32) (int, error) {
		if added >= n {
			return -1, syscall.ENOSPC
		}
		added++
		return syscall.InotifyAddWatch(fd, path, mask)
	}
	t.Cleanup(func() { inotifyAddWatch = syscall.InotifyAddWatch })
}

func TestLimitAtStart(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "a", "b"), 0o755)
	limitWatches(t, 2)
	if w, err := New(root, Options{}); !errors.Is(err, ErrLimit) {
		if w != nil {
			w.Close()
		}
		t.Fatalf("got %v, want ErrLimit", err)
	}
}

func TestLimitLater(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "a"), 0o755)
	limitWatches(t, 2)
	w, err := New(root, Options{Latency: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	os.MkdirAll(filepath.Join(root, "new"), 0o755)
	// The watcher says it stopped seeing everything, and asks for a look
	// at the whole tree.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case b := <-w.Events():
			if b.Overflow && errors.Is(w.Err(), ErrLimit) {
				return
			}
		case <-deadline:
			t.Fatalf("no overflow; err %v", w.Err())
		}
	}
}
