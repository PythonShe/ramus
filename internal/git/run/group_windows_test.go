//go:build windows

package run

import (
	"context"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"

	"ramus/internal/git/gitexe"
)

// git starts with no console window: the app has no console, so Windows
// would give each git one of its own.
func TestNoConsoleWindow(t *testing.T) {
	r := runner(t, 1)
	for _, c := range []*Cmd{{GitDir: t.TempDir(), Read: true, Args: []string{"version"}}, {GitDir: t.TempDir(), Args: []string{"version"}}} {
		cmd := r.command(context.Background(), c)
		leaveGroup(cmd)
		a := cmd.SysProcAttr
		if a == nil || !a.HideWindow || a.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
			t.Fatalf("%v: SysProcAttr %+v", c.Args, a)
		}
	}
}

// Only a read ends with the app on Windows: a fetch no one waits on is
// let end, as git stopped from outside leaves its locks, and so is a
// diff tool the user opened.
func TestOnlyReadsDieWithApp(t *testing.T) {
	r := runner(t, 1)
	for _, c := range []struct {
		cmd  *Cmd
		want bool
	}{
		{&Cmd{GitDir: t.TempDir(), Read: true, Args: []string{"version"}}, true},
		{&Cmd{GitDir: t.TempDir(), NoPrompt: true, EndsWithApp: true, Args: []string{"fetch"}}, false},
		{&Cmd{GitDir: t.TempDir(), NoPrompt: true, Args: []string{"difftool"}}, false},
		{&Cmd{GitDir: t.TempDir(), Args: []string{"commit"}}, false},
	} {
		cmd := r.Command(context.Background(), c.cmd)
		v, ok := groups.Load(cmd)
		leaveGroup(cmd)
		if !ok || v.(*gitexe.Job).Dies() != c.want {
			t.Errorf("%v: in a group %v, dies with the app %v, want %v", c.cmd.Args, ok, ok && v.(*gitexe.Job).Dies(), c.want)
		}
	}
}

// A variable that points git at a repository is dropped whatever the
// case of its name: git on Windows reads Git_Dir as GIT_DIR.
func TestLocalEnvAnyCase(t *testing.T) {
	r := New(&gitexe.Git{Path: "git"}, []string{`Git_Dir=C:\wrong`, `git_index_file=C:\wrong\index`, `Path=C:\bin`}, 1)
	if got := r.Environ(); len(got) != 1 || got[0] != `Path=C:\bin` {
		t.Fatalf("environment %q", got)
	}
}

// A command Run starts is created suspended, put in its job and only then
// let run, so that nothing it starts escapes the job.
func TestRunStartsInItsJob(t *testing.T) {
	r := runner(t, 1)
	cmd := r.command(context.Background(), &Cmd{GitDir: t.TempDir(), Read: true, Args: []string{"version"}})
	waited := watchGroup(cmd)
	if cmd.SysProcAttr.CreationFlags&windows.CREATE_SUSPENDED == 0 {
		t.Fatal("not created suspended")
	}
	if err := r.Start(cmd); err != nil {
		t.Fatal(err)
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	var in int32
	isProcessInJob := windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")
	if ok, _, err := isProcessInJob.Call(uintptr(h), 0, uintptr(unsafe.Pointer(&in))); ok == 0 {
		t.Fatal(err)
	}
	if in == 0 {
		t.Error("git is in no job")
	}
	// It runs to its end: it was let run.
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	waited()
}
