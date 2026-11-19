package askpass

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The folder an app that crashed left is removed; one an app listens on,
// one just made and one holding anything else stay.
func TestRemoveStale(t *testing.T) {
	// A short folder: a socket's path is limited to about a hundred bytes.
	tmp, err := os.MkdirTemp("", "st")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmp)
	old := time.Now().Add(-time.Hour)
	folder := func(name string, listening bool, age time.Time) string {
		t.Helper()
		dir := filepath.Join(tmp, name)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		ln, err := net.Listen("unix", filepath.Join(dir, "s"))
		if err != nil {
			t.Skip("no unix sockets:", err)
		}
		if listening {
			t.Cleanup(func() { ln.Close() })
		} else {
			ln.(*net.UnixListener).SetUnlinkOnClose(false)
			ln.Close()
		}
		os.Chtimes(dir, age, age)
		return dir
	}
	crashed := folder("ramus-crashed", false, old)
	live := folder("ramus-live", true, old)
	fresh := folder("ramus-fresh", false, time.Now())
	other := folder("ramus-other", false, old)
	os.WriteFile(filepath.Join(other, "x"), nil, 0o600)
	os.Chtimes(other, old, old)
	removeStale(tmp, "")
	if _, err := os.Stat(crashed); err == nil {
		t.Error("the crashed app's folder stayed")
	}
	for _, dir := range []string{live, fresh, other} {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("%s removed", filepath.Base(dir))
		}
	}
}
