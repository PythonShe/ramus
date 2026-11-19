//go:build windows

package run

import (
	"context"
	"testing"

	"golang.org/x/sys/windows"

	"ramus/internal/git/gitexe"
	"ramus/internal/git/gittest"
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
// let run, so that nothing it starts escapes the job. The job is the one
// made for it, asked for by its handle: on a CI runner the test binary is
// itself in a job, and so is every process it starts.
func TestRunStartsInItsJob(t *testing.T) {
	repo := gittest.Init(t)
	r := runner(t, 1)
	// It reads its input until the test ends it, so that it still runs,
	// in its job, when the test asks.
	cmd := r.command(context.Background(), &Cmd{GitDir: repo.GitDir(), Read: true, Args: []string{"cat-file", "--batch-check"}})
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	waited := watchGroup(cmd)
	if cmd.SysProcAttr.CreationFlags&windows.CREATE_SUSPENDED == 0 {
		t.Fatal("not created suspended")
	}
	v, ok := groups.Load(cmd)
	if !ok {
		t.Fatal("no job prepared")
	}
	job := v.(*gitexe.Job)
	if err := r.Start(cmd); err != nil {
		t.Fatal(err)
	}
	if err := r.JobError(); err != nil {
		t.Fatalf("no job: %v", err)
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	if in, err := job.Holds(h); err != nil || !in {
		t.Errorf("git in the job made for it: %v, %v", in, err)
	}
	// It runs to its end once its input does: it was let run.
	stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	waited()
}
