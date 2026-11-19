package crashlog

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A process of the app run apart from it, as git's difftool, catches its
// crash in a file of its own, which the app's next launch reports; one
// still running, which printed nothing, is left alone.
func TestFatalOfAnotherProcess(t *testing.T) {
	if dir := os.Getenv("CRASHLOG_OTHER"); dir != "" {
		if err := CatchFatalIn(dir, "tool"); err != nil {
			t.Fatal(err)
		}
		go func() { panic("the tool crashed") }()
		time.Sleep(10 * time.Second)
		return
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestFatalOfAnotherProcess$")
	cmd.Env = append(os.Environ(), "CRASHLOG_OTHER="+dir)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("the child did not crash: %s", out)
	}
	live := filepath.Join(dir, otherOutput+"askpass.txt")
	if err := os.WriteFile(live, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	l := New(dir, "")
	names, err := l.ReportFatal()
	if err != nil || len(names) != 1 {
		t.Fatalf("%q %v", names, err)
	}
	if data, _ := os.ReadFile(names[0]); !strings.Contains(string(data), "the tool crashed") {
		t.Fatalf("report:\n%s", data)
	}
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("a running process's file set aside: %v", err)
	}
	if again, err := l.ReportFatal(); err != nil || len(again) != 0 {
		t.Fatalf("again %q %v", again, err)
	}
}
