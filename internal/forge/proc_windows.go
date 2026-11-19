//go:build windows

package forge

import (
	"os/exec"
	"sync"
	"syscall"

	"golang.org/x/sys/windows"
)

// The job is the one internal/git/run puts git in; its helpers are its
// own, so the same is written here.

// runTool runs cmd with no window, in a job of its own (ownGroup).
func runTool(cmd *exec.Cmd) error {
	j := ownGroup(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	j.join(cmd.Process.Pid)
	return cmd.Wait()
}

// job is the job object the tool and everything it starts run in, which
// Windows passes on to each child as it is made: cancelling ends all of
// it, as a process group does elsewhere. Killing gh alone would leave
// what it started (git, under gh stack) holding the pipes read until
// WaitDelay.
type job struct {
	mu        sync.Mutex
	h         windows.Handle
	cancelled bool
}

// ownGroup gives cmd no console window: the app is a program of the
// desktop, with no console, and Windows gives a console program started
// from one a window of its own, which would flash up and take the focus
// at every call. What the tool starts shares its console, as hidden.
// Cancelling ends the job the tool is put in once started (join).
func ownGroup(cmd *exec.Cmd) *job {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	j := &job{}
	cmd.Cancel = func() error {
		j.mu.Lock()
		j.cancelled = true
		if j.h != 0 {
			windows.TerminateJobObject(j.h, 1)
		}
		j.mu.Unlock()
		return cmd.Process.Kill()
	}
	return j
}

// join puts the process just started, pid, in a job of its own.
func (j *job) join(pid int) {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return
	}
	p, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		windows.CloseHandle(h)
		return
	}
	if err := windows.AssignProcessToJobObject(h, p); err != nil {
		windows.CloseHandle(p)
		windows.CloseHandle(h)
		return
	}
	j.mu.Lock()
	j.h = h
	if j.cancelled {
		windows.TerminateJobObject(h, 1)
	}
	j.mu.Unlock()
	// Once the tool has exited, as on other systems, what it left running
	// is let be: the job is let go without ending it.
	go func() {
		windows.WaitForSingleObject(p, windows.INFINITE)
		windows.CloseHandle(p)
		j.mu.Lock()
		windows.CloseHandle(j.h)
		j.h = 0
		j.mu.Unlock()
	}()
}
