package statefile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// link makes name a symbolic link to dest, or skips the test where links
// cannot be made (Windows without the privilege).
func link(t *testing.T, dest, name string) {
	t.Helper()
	if err := os.Symlink(dest, name); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
}

// isLink fails the test unless name is still a symbolic link to dest.
func isLink(t *testing.T, name, dest string) {
	t.Helper()
	got, err := os.Readlink(name)
	if err != nil || got != dest {
		t.Fatalf("%s is no longer the link to %s: %q, %v", name, dest, got, err)
	}
}

// A state file linked into place from another folder, as dotfiles kept
// in a repository are, is written through the link: the file it leads to
// changes, the link stays, and nothing is left in either folder.
func TestWriteThroughALink(t *testing.T) {
	dots, app := t.TempDir(), t.TempDir()
	dest := filepath.Join(dots, "settings.json")
	os.WriteFile(dest, []byte("1\n"), 0o600)
	p := filepath.Join(app, "settings.json")
	link(t, dest, p)
	if err := WriteFile(p, []byte("2\n")); err != nil {
		t.Fatal(err)
	}
	isLink(t, p, dest)
	if data, _ := os.ReadFile(dest); string(data) != "2\n" {
		t.Fatalf("the linked file has %q", data)
	}
	for _, dir := range []string{dots, app} {
		if names, _ := os.ReadDir(dir); len(names) != 1 {
			t.Fatalf("left in %s: %v", dir, names)
		}
	}
	// A relative link, through a second link.
	second := filepath.Join(app, "second.json")
	link(t, "settings.json", second)
	if err := WriteFile(second, []byte("3\n")); err != nil {
		t.Fatal(err)
	}
	isLink(t, second, "settings.json")
	isLink(t, p, dest)
	if data, _ := os.ReadFile(dest); string(data) != "3\n" {
		t.Fatalf("the linked file has %q", data)
	}
}

// A link that leads to no file has the file made where it leads, when
// that folder is there; a folder that is not there is not made, and the
// write fails with the link left as it was.
func TestWriteThroughADanglingLink(t *testing.T) {
	dots, app := t.TempDir(), t.TempDir()
	dest := filepath.Join(dots, "settings.json")
	p := filepath.Join(app, "settings.json")
	link(t, dest, p)
	if err := WriteFile(p, []byte("1\n")); err != nil {
		t.Fatal(err)
	}
	isLink(t, p, dest)
	if data, _ := os.ReadFile(dest); string(data) != "1\n" {
		t.Fatalf("the linked file has %q", data)
	}
	gone := filepath.Join(dots, "not-cloned", "settings.json")
	q := filepath.Join(app, "layout.json")
	link(t, gone, q)
	if err := WriteFile(q, []byte("1\n")); err == nil {
		t.Fatal("a folder made for a link that leads nowhere")
	}
	isLink(t, q, gone)
	if _, err := os.Stat(filepath.Dir(gone)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the folder was made: %v", err)
	}
}

// Links that lead back to themselves fail the write, which leaves them,
// and are not kept aside either: there is no file to move.
func TestWriteThroughALinkLoop(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json")
	link(t, b, a)
	link(t, a, b)
	if err := WriteFile(a, []byte("1\n")); !errors.Is(err, ErrLinkLoop) {
		t.Fatalf("err = %v, want ErrLinkLoop", err)
	}
	isLink(t, a, b)
	isLink(t, b, a)
	if e := Unreadable(a, errors.New("bad")); e.KeepErr == nil || e.KeptAs != "" {
		t.Fatalf("kept aside: %+v", e)
	}
	isLink(t, a, b)
}

// A link to a file in a folder that cannot be written fails the write,
// and leaves the link and the file as they were.
func TestWriteThroughALinkToAReadOnlyFolder(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("a folder's mode does not stop its owner here")
	}
	dots, app := t.TempDir(), t.TempDir()
	dest := filepath.Join(dots, "settings.json")
	os.WriteFile(dest, []byte("1\n"), 0o600)
	p := filepath.Join(app, "settings.json")
	link(t, dest, p)
	if err := os.Chmod(dots, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dots, 0o700) })
	if err := WriteFile(p, []byte("2\n")); err == nil {
		t.Fatal("written into a folder that cannot be")
	}
	isLink(t, p, dest)
	if data, _ := os.ReadFile(dest); string(data) != "1\n" {
		t.Fatalf("the linked file has %q", data)
	}
}

// A linked file that does not read is kept aside beside itself, and the
// link stays for the next write to make it again.
func TestKeptAsideThroughALink(t *testing.T) {
	dots, app := t.TempDir(), t.TempDir()
	dest := filepath.Join(dots, "settings.json")
	os.WriteFile(dest, []byte("{"), 0o600)
	p := filepath.Join(app, "settings.json")
	link(t, dest, p)
	e := Unreadable(p, errors.New("bad"))
	if e.KeepErr != nil || e.KeptAs != dest+".broken" {
		t.Fatalf("kept %+v", e)
	}
	isLink(t, p, dest)
	if data, _ := os.ReadFile(dest + ".broken"); string(data) != "{" {
		t.Fatalf("kept file has %q", data)
	}
	if err := WriteFile(p, []byte("{}\n")); err != nil {
		t.Fatal(err)
	}
	isLink(t, p, dest)
	e = PartlyRead(p, []byte(`{"a": 1}`), errors.New("bad"))
	if e.KeepErr != nil || e.KeptAs != dest+".broken-2" {
		t.Fatalf("copy kept %+v", e)
	}
}
