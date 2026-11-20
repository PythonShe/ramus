//go:build windows

package gitexe

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var comspec = filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")

// A process started suspended stays so until Join has put it in the job
// Join made, then runs. The job is asked for by its handle: a test binary
// that itself runs in a job, as on a CI runner, has every child in that
// one from the start.
func TestJobHoldsTheProcessFromTheStart(t *testing.T) {
	var out bytes.Buffer
	// It waits for its input to end, so that it still runs, in its job,
	// when the test asks. Made with a context, as the app makes every
	// command: InJob sets Cancel, which Start refuses on a command made
	// without one.
	cmd := exec.CommandContext(context.Background(), comspec, "/c", "set /p x=& echo answered")
	cmd.Stdout = &out
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	j := InJob(cmd, false)
	j.Suspend(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// On a failure before Join it is still suspended: killed, not waited
	// for good.
	defer cmd.Wait()
	defer cmd.Process.Kill()
	defer stdin.Close()
	pid := uint32(cmd.Process.Pid)
	if n := suspendCount(t, pid); n == 0 {
		t.Fatal("not created suspended: it may start what escapes the job")
	}
	if err := j.Join(cmd); err != nil {
		t.Fatal(err)
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	if in, err := j.Holds(h); err != nil || !in {
		t.Fatalf("in the job Join made: %v, %v", in, err)
	}
	stdin.Close()
	// Its exit code may be that of set /p, which fails on an input that
	// ends empty: what it printed says it ran to its end.
	if err := cmd.Wait(); !bytes.Contains(out.Bytes(), []byte("answered")) {
		t.Fatalf("%v: %q", err, out.String())
	}
}

// A command cancelled as it starts, before Join, is ended by the cancel,
// and Join, which then cannot put it in its job nor resume it, says
// nothing: the runner would keep the error as why git runs without a job.
func TestJoinOfACancelledCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, comspec, "/c", "exit")
	j := InJob(cmd, false)
	j.Suspend(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(cmd.Process.Pid))
	if err != nil {
		cmd.Process.Kill()
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	cancel()
	if ev, err := windows.WaitForSingleObject(h, 10_000); ev != windows.WAIT_OBJECT_0 {
		cmd.Process.Kill()
		t.Fatalf("not ended by the cancel: %d, %v", ev, err)
	}
	if err := j.Join(cmd); err != nil {
		t.Fatalf("Join of a command cancelled: %v", err)
	}
}

var suspendThread = windows.NewLazySystemDLL("kernel32.dll").NewProc("SuspendThread")

// suspendCount returns how many times the first thread of process pid is
// suspended, leaving it so.
func suspendCount(t *testing.T, pid uint32) uint32 {
	t.Helper()
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(snap)
	te := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for e := windows.Thread32First(snap, &te); e == nil; e = windows.Thread32Next(snap, &te) {
		if te.OwnerProcessID != pid {
			continue
		}
		th, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, te.ThreadID)
		if err != nil {
			t.Fatal(err)
		}
		defer windows.CloseHandle(th)
		// SuspendThread returns the count before it, or (DWORD)-1.
		n, _, err := suspendThread.Call(uintptr(th))
		if uint32(n) == ^uint32(0) {
			t.Fatal(err)
		}
		if _, err := windows.ResumeThread(th); err != nil {
			t.Fatal(err)
		}
		return uint32(n)
	}
	t.Fatal("no thread")
	return 0
}

// Where no job could be had, cancelling still ends what the process
// started, which would otherwise hold its output open until WaitDelay.
func TestNoJobEndsTheTree(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, comspec)
	// A second cmd, the process's child, says it runs with a builtin,
	// which writes at once whatever its output is, then waits for input
	// that the test ends only once it has failed, holding the output.
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: comspec + ` /d /c ` + comspec + ` /d /c "echo started& set /p x="`}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	cmd.WaitDelay = 30 * time.Second
	j := InJob(cmd, false)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	j.fallBack(uint32(cmd.Process.Pid))
	sc := bufio.NewScanner(out)
	started := false
	for !started && sc.Scan() {
		started = strings.Contains(sc.Text(), "started")
	}
	if !started {
		t.Fatalf("the child never ran: %v", sc.Err())
	}
	cancel()
	// The output closes once all that held it has ended. Read apart and
	// bounded: WaitDelay does not close a pipe the caller reads, so a
	// child left running would hold the read past the test's own end.
	copied := make(chan struct{})
	go func() {
		io.Copy(io.Discard, out)
		close(copied)
	}()
	select {
	case <-copied:
	case <-time.After(time.Minute):
		// The child left running reads the input: at its end it exits,
		// and lets the output go. A close of the output would not end
		// the read, which holds the pipe on Windows.
		stdin.Close()
		<-copied
		cmd.Wait()
		t.Fatal("15s after the cancel, what the process started still held its output")
	}
	cmd.Wait()
}
