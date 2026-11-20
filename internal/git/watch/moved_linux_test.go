package watch

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// A folder moved out of the root and back, both in one read of the
// queue, keeps its watches: the return finds the same watches again,
// which the departure must not then remove.
func TestMovedOutAndBackInOneRead(t *testing.T) {
	root := t.TempDir()
	away := t.TempDir()
	os.MkdirAll(filepath.Join(root, "a", "b"), 0o755)
	w := &Watcher{root: root, notify: make(chan struct{}, 1), pending: map[string]struct{}{}}
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC | syscall.IN_NONBLOCK)
	if err != nil {
		t.Fatal(err)
	}
	// No reader runs: the test reads the queue itself, once, after both
	// moves, so that they are in one read.
	l := &linuxWatch{w: w, dirs: map[int32]string{}, file: os.NewFile(uintptr(fd), "inotify")}
	defer l.file.Close()
	if err := l.addTree("."); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "a"), filepath.Join(away, "a")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(away, "a"), filepath.Join(root, "a")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64<<10)
	n, err := syscall.Read(fd, buf)
	if err != nil {
		t.Fatal(err)
	}
	l.handle(buf[:n])
	l.mu.Lock()
	var watched []string
	for _, d := range l.dirs {
		watched = append(watched, d)
	}
	l.mu.Unlock()
	for _, want := range []string{".", "a", "a/b"} {
		if !slices.Contains(watched, want) {
			t.Errorf("%s is not watched: %v", want, watched)
		}
	}
	// And the system still reports what changes there.
	if err := os.WriteFile(filepath.Join(root, "a", "b", "f"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// A removed watch queues an event of its own, so the queue is read
	// until the file's event, which the write queued, or its end.
	for {
		pfd := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		if k, err := unix.Poll(pfd, 10_000); err != nil || k != 1 {
			t.Fatalf("no event for a file made in a/b: %d, %v", k, err)
		}
		n, err := syscall.Read(fd, buf)
		if err != nil {
			t.Fatal(err)
		}
		for off := 0; off+syscall.SizeofInotifyEvent <= n; {
			ev := (*syscall.InotifyEvent)(unsafe.Pointer(&buf[off]))
			name := strings.TrimRight(string(buf[off+syscall.SizeofInotifyEvent:off+syscall.SizeofInotifyEvent+int(ev.Len)]), "\x00")
			off += syscall.SizeofInotifyEvent + int(ev.Len)
			if name == "f" {
				return
			}
		}
	}
}

// A folder renamed in the root, in one read with what follows: it keeps
// its watches, under its new name; and when it is then moved out of the
// root in the same read, its watches go, though they were never named
// by the name it left by.
func TestRenamedInOneRead(t *testing.T) {
	for _, out := range []bool{false, true} {
		root := t.TempDir()
		away := t.TempDir()
		os.MkdirAll(filepath.Join(root, "a", "sub"), 0o755)
		w := &Watcher{root: root, notify: make(chan struct{}, 1), pending: map[string]struct{}{}}
		fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC | syscall.IN_NONBLOCK)
		if err != nil {
			t.Fatal(err)
		}
		l := &linuxWatch{w: w, dirs: map[int32]string{}, file: os.NewFile(uintptr(fd), "inotify")}
		defer l.file.Close()
		if err := l.addTree("."); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(filepath.Join(root, "a"), filepath.Join(root, "b")); err != nil {
			t.Fatal(err)
		}
		want := []string{".", "b", "b/sub"}
		if out {
			if err := os.Rename(filepath.Join(root, "b"), filepath.Join(away, "b")); err != nil {
				t.Fatal(err)
			}
			want = []string{"."}
		}
		buf := make([]byte, 64<<10)
		n, err := syscall.Read(fd, buf)
		if err != nil {
			t.Fatal(err)
		}
		l.handle(buf[:n])
		l.mu.Lock()
		var watched []string
		for _, d := range l.dirs {
			watched = append(watched, d)
		}
		l.mu.Unlock()
		slices.Sort(watched)
		if !slices.Equal(watched, want) {
			t.Errorf("moved out %v: watched %v, want %v", out, watched, want)
		}
	}
}
