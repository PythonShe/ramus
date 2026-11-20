package watch

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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

// A directory SkipDir named has no watch until Rewatch, once SkipDir no
// longer names it.
func TestRewatch(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "out", "deep"), 0o755)
	var skipping atomic.Bool
	skipping.Store(true)
	w, err := New(root, Options{Latency: 20 * time.Millisecond, SkipDir: func(rel string) bool { return skipping.Load() && rel == "out" }})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ready(t, w, root)
	os.WriteFile(filepath.Join(root, "out", "deep", "skipped"), []byte("x"), 0o644)
	select {
	case b := <-w.Events():
		t.Fatalf("a change in a skipped directory: %+v", b)
	case <-time.After(300 * time.Millisecond):
	}
	skipping.Store(false)
	w.Rewatch("out")
	os.WriteFile(filepath.Join(root, "out", "deep", "seen"), []byte("x"), 0o644)
	wait(t, w, "out/deep/seen")
}

// A folder the user cannot read, there at start or made later, is left
// unwatched; the watcher goes on with the rest.
func TestUnreadableFolder(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every folder")
	}
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	os.MkdirAll(filepath.Join(locked, "in"), 0o755)
	os.Chmod(locked, 0)
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	w, err := New(root, Options{Latency: 20 * time.Millisecond})
	if err != nil {
		t.Fatalf("an unreadable folder stopped the watcher: %v", err)
	}
	defer w.Close()
	ready(t, w, root)
	later := filepath.Join(root, "later")
	os.Mkdir(later, 0)
	t.Cleanup(func() { os.Chmod(later, 0o755) })
	wait(t, w, "later")
	os.WriteFile(filepath.Join(root, "f"), []byte("x"), 0o644)
	wait(t, w, "f")
	if err := w.Err(); err != nil {
		t.Errorf("the watcher says it stopped: %v", err)
	}
}

// A folder moved out of the tree loses its watches, so what changes in
// it there is not reported here; one moved within the tree is watched at
// its new place.
func TestMovedFolders(t *testing.T) {
	root := t.TempDir()
	away := t.TempDir()
	os.MkdirAll(filepath.Join(root, "out", "deep"), 0o755)
	os.MkdirAll(filepath.Join(root, "in", "deep"), 0o755)
	w, err := New(root, Options{Latency: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	l := w.plat.(*linuxWatch)
	ready(t, w, root)
	os.Rename(filepath.Join(root, "in"), filepath.Join(root, "moved"))
	wait(t, w, "moved")
	if err := os.Rename(filepath.Join(root, "out"), filepath.Join(away, "out")); err != nil {
		t.Fatal(err)
	}
	wait(t, w, "out")
	ready(t, w, root)
	os.WriteFile(filepath.Join(away, "out", "deep", "f"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, "moved", "deep", "g"), []byte("y"), 0o644)
	for seen, deadline := false, time.After(time.Minute); !seen; {
		select {
		case b := <-w.Events():
			for _, p := range b.Paths {
				if strings.HasPrefix(p, "out") {
					t.Errorf("a change outside the tree reported as %s", p)
				}
				seen = seen || p == "moved/deep/g"
			}
		case <-deadline:
			t.Fatal("no event for moved/deep/g")
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, rel := range l.dirs {
		if rel == "out" || strings.HasPrefix(rel, "out/") || rel == "in" || strings.HasPrefix(rel, "in/") {
			t.Errorf("a watch left at %s", rel)
		}
	}
}

// Once closed, the watcher adds no watch: the descriptor's number may be
// another file's by then.
func TestNoWatchAfterClose(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "a"), 0o755)
	w, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	l := w.plat.(*linuxWatch)
	w.Close()
	var calls atomic.Int32
	inotifyAddWatch = func(fd int, path string, mask uint32) (int, error) {
		calls.Add(1)
		return syscall.InotifyAddWatch(fd, path, mask)
	}
	t.Cleanup(func() { inotifyAddWatch = syscall.InotifyAddWatch })
	if err := l.addTree("."); err != nil {
		t.Errorf("after close: %v", err)
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("%d watches added after close", n)
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
	deadline := time.After(time.Minute)
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
