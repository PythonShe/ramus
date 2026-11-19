package crashlog

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/lifetime"
	"ramus/internal/git/run"
)

func TestPanicReport(t *testing.T) {
	r := gittest.Init(t)
	rn := testrun.Runner(t)
	rn.Run(context.Background(), &run.Cmd{GitDir: r.GitDir(), Read: true, Args: []string{"rev-parse", "--verify", "https://u:hunter2@example.com/x"}})
	home, _ := os.UserHomeDir()
	dir := t.TempDir()
	l := New(dir, "1.2.3")
	l.SetGit(rn.Git(), rn.Journal())
	name, err := l.Panic(filepath.Join(home, "src", "repo"), r.GitDir(), lifetime.Panic{
		Value: "boom https://u:hunter2@example.com/x", Stack: "goroutine 7 [running]:\n" + home + "/x.go:12\n"})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(name)
	text := string(data)
	for _, want := range []string{"Ramus: 1.2.3", "OS: ", "Git: " + rn.Git().Version.Raw, "Repository: " + filepath.Join("~", "src", "repo"), "Panic: boom", "goroutine 7", "~/x.go:12", "git rev-parse --verify", "exit 128"} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in\n%s", want, text)
		}
	}
	if strings.Contains(text, "hunter2") || home != "" && strings.Contains(text, home+"/") {
		t.Errorf("kept a secret or the home folder:\n%s", text)
	}
	if st, _ := os.Stat(name); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode())
	}
}

func TestPrune(t *testing.T) {
	l := New(t.TempDir(), "")
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range MaxReports + 5 {
		if _, err := l.write(Report{Time: start.Add(time.Duration(i) * time.Second), What: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	names, _ := l.Reports()
	if len(names) != MaxReports || !strings.Contains(names[0], "00-00-24") {
		t.Fatalf("%d kept, newest %s", len(names), filepath.Base(names[0]))
	}
}

// Two reports in the same millisecond keep two files, and the same panic
// again within a minute writes none.
func TestReportsKept(t *testing.T) {
	l := New(t.TempDir(), "")
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a, err := l.write(Report{Time: at, What: "a"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := l.write(Report{Time: at, What: "b"})
	if err != nil || a == b {
		t.Fatalf("%s and %s: %v", a, b, err)
	}
	if first, _ := os.ReadFile(a); !strings.Contains(string(first), "\na\n") {
		t.Fatalf("the first report overwritten:\n%s", first)
	}
	for i, want := range []bool{true, false, true} {
		value := "boom"
		if i == 2 {
			value = "other"
		}
		name, err := l.Panic("", "", lifetime.Panic{Value: value, Stack: "stack"})
		if err != nil || (name != "") != want {
			t.Fatalf("panic %d (%s): %q %v", i, value, name, err)
		}
	}
}

// On Windows the home folder is taken out however git or Go spelled it.
func TestWindowsHomeTakenOut(t *testing.T) {
	l := &Log{home: `C:\Users\First Last`, fold: true}
	got := l.clean(`C:\Users\First Last\a c:/users/first last/b C:/Users/First Last/c`)
	if want := `~\a ~/b ~/c`; got != want {
		t.Fatalf("%q, want %q", got, want)
	}
}

// TestFatal crashes a child process and reads its report at the next
// "launch".
func TestFatal(t *testing.T) {
	if dir := os.Getenv("CRASHLOG_CHILD"); dir != "" {
		if _, err := New(dir, "").CatchFatal(); err != nil {
			t.Fatal(err)
		}
		go func() { panic("fatal https://u:hunter2@example.com/x") }()
		time.Sleep(10 * time.Second)
		return
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestFatal$")
	cmd.Env = append(os.Environ(), "CRASHLOG_CHILD="+dir)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("the child did not crash: %s", out)
	}
	l := New(dir, "")
	name, err := l.CatchFatal()
	if err != nil || name == "" {
		t.Fatalf("%q %v", name, err)
	}
	defer l.stopFatal()
	data, _ := os.ReadFile(name)
	text := string(data)
	if !strings.Contains(text, "fatal error") && !strings.Contains(text, "panic: fatal") || strings.Contains(text, "hunter2") || !strings.Contains(text, "goroutine") {
		t.Fatalf("report:\n%s", text)
	}
	// Read once: the next launch finds nothing.
	if again, err := l.CatchFatal(); err != nil || again != "" {
		t.Fatalf("again %q %v", again, err)
	}
}
