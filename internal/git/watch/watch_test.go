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

// ready writes a file until the watcher reports it, so the watch has
// surely started however long the system takes to start it, then drains
// batches until none came for a while.
func ready(t *testing.T, w *Watcher, root string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for got := false; !got; {
		os.WriteFile(filepath.Join(root, ".ready"), []byte(time.Now().String()), 0o644)
		select {
		case b := <-w.Events():
			got = b.Overflow || slices.Contains(b.Paths, ".ready")
		case <-time.After(200 * time.Millisecond):
		}
		if !got && time.Now().After(deadline) {
			t.Fatal("the watch never started")
		}
	}
	for {
		select {
		case <-w.Events():
		case <-time.After(300 * time.Millisecond):
			return
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
	ready(t, w, root)
	os.WriteFile(filepath.Join(root, "a", "b", "f.txt"), []byte("x"), 0o644)
	wait(t, w, "a/b/f.txt")
	// A directory made after the watch started is watched too.
	os.MkdirAll(filepath.Join(root, "new", "deep"), 0o755)
	wait(t, w, "new") // seen, and watched where watches are per directory
	os.WriteFile(filepath.Join(root, "new", "deep", "g.txt"), []byte("y"), 0o644)
	wait(t, w, "new/deep/g.txt")
	os.Remove(filepath.Join(root, "a", "b", "f.txt"))
	wait(t, w, "a/b/f.txt")
	os.Rename(filepath.Join(root, "new"), filepath.Join(root, "renamed"))
	wait(t, w, "renamed")
}

// A file written in a folder, by a write and by a rename over it as git
// writes its index, is reported, not the folder: a folder reported means
// everything in it to be read again.
func TestFileNotItsFolder(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "sub"), 0o755)
	w, err := New(root, Options{Latency: 20 * time.Millisecond})
	if err == ErrUnsupported {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ready(t, w, root)
	os.WriteFile(filepath.Join(root, "sub", "f"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, "sub", "g.lock"), []byte("y"), 0o644)
	os.Rename(filepath.Join(root, "sub", "g.lock"), filepath.Join(root, "sub", "g"))
	seen := map[string]bool{}
	deadline := time.After(5 * time.Second)
	for !seen["sub/f"] || !seen["sub/g"] {
		select {
		case b := <-w.Events():
			if b.Overflow {
				t.Fatal("overflow")
			}
			for _, p := range b.Paths {
				seen[p] = true
			}
		case <-deadline:
			t.Fatalf("saw %v", seen)
		}
	}
	// What else the system reports comes with the files, or soon after.
	for quiet := time.After(300 * time.Millisecond); ; {
		select {
		case b := <-w.Events():
			for _, p := range b.Paths {
				seen[p] = true
			}
			continue
		case <-quiet:
		}
		break
	}
	if seen["sub"] {
		t.Errorf("the folder was reported: %v", seen)
	}
}

// A root named in another letter case than the disk holds, where the
// file system ignores case, is watched as the folder it names.
func TestRootInAnotherCase(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "CaseRoot")
	os.Mkdir(dir, 0o755)
	other := filepath.Join(base, "caseroot")
	if _, err := os.Stat(other); err != nil {
		t.Skip("the file system keeps case apart")
	}
	w, err := New(other, Options{Latency: 20 * time.Millisecond})
	if err == ErrUnsupported {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if filepath.Base(w.Root()) != "CaseRoot" {
		t.Errorf("root %s", w.Root())
	}
	ready(t, w, dir)
	os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644)
	wait(t, w, "f")
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
	ready(t, w, root)
	left := map[string]bool{}
	start := time.Now()
	for i := 0; i < 200; i++ {
		name := "f" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644)
		left[name] = true
	}
	wrote := time.Since(start)
	batches := 0
	for deadline := time.After(10 * time.Second); len(left) > 0; {
		select {
		case b := <-w.Events():
			batches++
			if b.Overflow {
				return
			}
			for _, p := range b.Paths {
				delete(left, p)
			}
		case <-deadline:
			t.Fatalf("%d files never reported", len(left))
		}
	}
	// A batch per latency the writes took, and one more where they
	// straddle one: a batch per event would be hundreds.
	if limit := 2 + int(wrote/(200*time.Millisecond)); batches > limit {
		t.Errorf("burst split into %d batches, writes took %v", batches, wrote)
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
	// Goroutines end a while after they are done with, longer on a busy
	// machine.
	for deadline := time.Now().Add(5 * time.Second); runtime.NumGoroutine() > before+1; {
		if time.Now().After(deadline) {
			t.Errorf("%d goroutines, %d before", runtime.NumGoroutine(), before)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
}
