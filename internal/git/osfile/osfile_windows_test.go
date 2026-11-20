package osfile

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// git renames a lock file over the file, then deletes it, both while it
// is open here, as it does to an index, a ref or a commit-graph layer.
// Both as git for Windows does: the rename, from git 2.48, with POSIX
// semantics (FileRenameInfoEx), which replace a file open elsewhere when
// it was opened sharing deletion, on NTFS from Windows 10 1607; the
// delete with DeleteFileW, which uses POSIX semantics from Windows 11 and
// Server 2025 on, as git's compat/mingw.c notes. Older systems refuse
// both while the file is open, and git there retries until it is closed.
func TestRenamedOverAndDeletedWhileOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "packed-refs")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if plain, err := os.Open(path); err == nil {
		// What the opener exists for: os.Open does not share deletion.
		if os.Rename(path, path+".moved") == nil {
			t.Log("os.Open shares deletion on this system")
			os.Rename(path+".moved", path)
		}
		plain.Close()
	}
	f, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	lock := path + ".lock"
	if err := os.WriteFile(lock, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := renameOver(lock, path); err != nil {
		t.Fatalf("rename over a file open here: %v", err)
	}
	g, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if err := os.Remove(path); err != nil {
		t.Fatalf("delete a file open here: %v", err)
	}
	if b, err := io.ReadAll(f); err != nil || string(b) != "old" {
		t.Fatalf("the first file read %q, %v", b, err)
	}
	if b, err := io.ReadAll(g); err != nil || string(b) != "new" {
		t.Fatalf("the second file read %q, %v", b, err)
	}
}

// renameOver renames from over to as git for Windows does (compat/mingw.c,
// mingw_rename): through a handle with DELETE access, with
// FILE_RENAME_FLAG_REPLACE_IF_EXISTS and FILE_RENAME_FLAG_POSIX_SEMANTICS.
// MoveFileExW, which os.Rename calls, refuses while to is open anywhere.
func renameOver(from, to string) error {
	p, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(p, windows.DELETE|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	name, err := windows.UTF16FromString(to)
	if err != nil {
		return err
	}
	// FILE_RENAME_INFO, its union of ReplaceIfExists and Flags as Flags.
	type renameInfo struct {
		Flags          uint32
		RootDirectory  windows.Handle
		FileNameLength uint32
		FileName       [1]uint16
	}
	const replaceIfExists, posixSemantics = 0x1, 0x2
	size := unsafe.Offsetof(renameInfo{}.FileName) + uintptr(len(name))*2
	buf := make([]uint64, (size+7)/8) // aligned as the struct is
	info := (*renameInfo)(unsafe.Pointer(&buf[0]))
	info.Flags = replaceIfExists | posixSemantics
	info.FileNameLength = uint32(len(name)-1) * 2 // without the NUL
	copy(unsafe.Slice(&info.FileName[0], len(name)), name)
	return windows.SetFileInformationByHandle(h, windows.FileRenameInfoEx, (*byte)(unsafe.Pointer(info)), uint32(size))
}

// A device or a named pipe is refused without waiting on it.
func TestRefusesWhatIsNoFile(t *testing.T) {
	name := fmt.Sprintf(`\\.\pipe\ramus-osfile-%d`, os.Getpid())
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateNamedPipe(p, windows.PIPE_ACCESS_DUPLEX,
		windows.PIPE_TYPE_BYTE|windows.PIPE_WAIT, windows.PIPE_UNLIMITED_INSTANCES, 512, 512, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	for _, path := range []string{"NUL", name} {
		done := make(chan error, 1)
		go func() { _, err := ReadFile(path, 1<<20); done <- err }()
		select {
		case err := <-done:
			// The pipe may be refused by the system before the check
			// sees it; what matters is that nothing is read from it.
			if err == nil || path == "NUL" && !errors.Is(err, ErrNotRegular) {
				t.Errorf("%s: %v", path, err)
			}
		case <-time.After(time.Minute):
			t.Fatalf("%s: still waiting", path)
		}
	}
}

// A path past MAX_PATH opens, as it does with os.Open.
func TestLongPath(t *testing.T) {
	dir, err := filepath.Abs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Made and removed through the \\?\ form, which takes a path past
	// MAX_PATH whether or not the system turned long paths on: Go, which
	// marks the process long-path aware, adds no prefix itself, so the
	// plain form would fail where LongPathsEnabled is 0, and so would the
	// removal of the test's folder.
	t.Cleanup(func() { os.RemoveAll(`\\?\` + dir) })
	deep := dir
	for _, name := range []string{strings.Repeat("d", 100), strings.Repeat("e", 100), strings.Repeat("f", 100)} {
		deep = filepath.Join(deep, name)
		if err := os.Mkdir(`\\?\`+deep, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(deep, "HEAD")
	want := []byte("ref: refs/heads/main\n")
	if err := os.WriteFile(`\\?\`+path, want, 0o644); err != nil {
		t.Fatal(err)
	}
	if len(path) <= 300 {
		t.Fatalf("a path of %d characters", len(path))
	}
	// Spelled as the system would clean it: forward slashes, a step out
	// and back.
	odd := filepath.ToSlash(deep + `\..\` + filepath.Base(deep) + `\.\HEAD`)
	for _, p := range []string{path, odd} {
		if b, err := ReadFile(p, 1<<10); err != nil || !bytes.Equal(b, want) {
			t.Fatalf("%s: read %q, %v", p, b, err)
		}
		f, err := Open(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		f.Close()
	}
}
