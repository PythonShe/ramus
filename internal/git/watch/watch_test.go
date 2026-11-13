package watch

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// wait collects batches until one names want or the time runs out.
func wait(t *testing.T, w *Watcher, want string) Batch {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case b, ok := <-w.Events():
			if !ok {
				t.Fatal("events closed")
			}
			if b.Overflow || slices.ContainsFunc(b.Paths, func(p string) bool { return p == want || strings.HasPrefix(want, p+"/") }) {
				return b
			}
		case <-deadline:
			t.Fatalf("no event for %s", want)
		}
	}
}

func TestWatch(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "a", "b"), 0o755)
	os.MkdirAll(filepath.Join(root, "skip"), 0o755)
	w, err := New(root, Options{Latency: 20 * time.Millisecond, SkipDir: func(rel string) bool { return rel == "skip" }})
	if err == ErrUnsupported {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	time.Sleep(100 * time.Millisecond) // let the stream start
	os.WriteFile(filepath.Join(root, "a", "b", "f.txt"), []byte("x"), 0o644)
	wait(t, w, "a/b/f.txt")
	// A directory made after the watch started is watched too.
	os.MkdirAll(filepath.Join(root, "new", "deep"), 0o755)
	time.Sleep(50 * time.Millisecond)
	os.WriteFile(filepath.Join(root, "new", "deep", "g.txt"), []byte("y"), 0o644)
	wait(t, w, "new/deep/g.txt")
	os.Remove(filepath.Join(root, "a", "b", "f.txt"))
	wait(t, w, "a/b/f.txt")
	os.Rename(filepath.Join(root, "new"), filepath.Join(root, "renamed"))
	wait(t, w, "renamed")
}

func TestBurstIsOneBatch(t *testing.T) {
	root := t.TempDir()
	w, err := New(root, Options{Latency: 200 * time.Millisecond})
	if err == ErrUnsupported {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	time.Sleep(100 * time.Millisecond)
	for i := 0; i < 200; i++ {
		os.WriteFile(filepath.Join(root, "f"+string(rune('a'+i%26))+string(rune('a'+i/26))), []byte("x"), 0o644)
	}
	b := wait(t, w, "faa")
	if !b.Overflow && len(b.Paths) < 100 {
		t.Errorf("burst split: first batch has %d paths", len(b.Paths))
	}
}

func TestClose(t *testing.T) {
	before := runtime.NumGoroutine()
	for i := 0; i < 20; i++ {
		w, err := New(t.TempDir(), Options{})
		if err == ErrUnsupported {
			t.Skip(err)
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if _, ok := <-w.Events(); ok {
			t.Fatal("events open after close")
		}
	}
	time.Sleep(50 * time.Millisecond)
	if n := runtime.NumGoroutine(); n > before+1 {
		t.Errorf("%d goroutines, %d before", n, before)
	}
}
