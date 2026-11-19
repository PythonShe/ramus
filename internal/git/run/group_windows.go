//go:build windows

package run

import (
	"errors"
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"

	"ramus/internal/git/gitexe"
)

// stopsHard: cancelling ends git with TerminateJobObject, which leaves it
// no chance to remove its lock files, as it does on SIGTERM elsewhere.
const stopsHard = true

// lower leaves the priority as it is: Windows has no nice.
func lower(cmd *exec.Cmd) {}

// group is the job object a process and everything it starts run in,
// which Windows passes on to each child as it is made: cancelling ends
// all of it, as a process group does elsewhere. Killing git alone would
// leave a hook's shell and what that runs going, holding the pipes the
// app reads until WaitDelay, and the folders they run in.
type group struct {
	mu        sync.Mutex
	job       windows.Handle
	cancelled bool
	// suspended is a process created suspended, so that it is in its job
	// before it runs (see watchGroup).
	suspended bool
	// dies ends the job with the app, should it end while git runs.
	dies bool
}

// groups holds the group of each command prepared and not started yet.
var groups sync.Map // *exec.Cmd -> *group

func stopGroup(cmd *exec.Cmd, dies bool) {
	g := &group{dies: dies}
	groups.Store(cmd, g)
	gitexe.Hide(cmd)
	cmd.Cancel = func() error {
		g.mu.Lock()
		g.cancelled = true
		if g.job != 0 {
			windows.TerminateJobObject(g.job, 1)
		}
		g.mu.Unlock()
		return cmd.Process.Kill()
	}
}

// joinGroup puts a process just started in its job. For a process created
// suspended it then lets it run, so what it starts is in the job from the
// first; another may already have started something, as the git.exe in
// Git\cmd starts the real git at once, which then escapes the job.
func joinGroup(cmd *exec.Cmd) {
	v, ok := groups.LoadAndDelete(cmd)
	if !ok {
		return
	}
	g := v.(*group)
	if g.suspended {
		defer func() {
			if err := resume(uint32(cmd.Process.Pid)); err != nil {
				// Never left suspended: Wait would wait until the timeout.
				cmd.Process.Kill()
			}
		}()
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return
	}
	if g.dies {
		// The app holds the job's one handle: when it ends, crashed or
		// killed, the handle closes and the job ends with it.
		killOnClose(job, true)
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, uint32(cmd.Process.Pid))
	if err != nil {
		windows.CloseHandle(job)
		return
	}
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		windows.CloseHandle(h)
		windows.CloseHandle(job)
		return
	}
	g.mu.Lock()
	g.job = job
	if g.cancelled {
		windows.TerminateJobObject(job, 1)
	}
	g.mu.Unlock()
	// Once git has exited, as on other systems, what it left running is
	// let be: the job is let go without ending it.
	go func() {
		windows.WaitForSingleObject(h, windows.INFINITE)
		windows.CloseHandle(h)
		g.mu.Lock()
		if g.dies {
			killOnClose(g.job, false)
		}
		windows.CloseHandle(g.job)
		g.job = 0
		g.mu.Unlock()
	}()
}

// watchGroup has cmd, one Run starts and waits on, created suspended, for
// joinGroup to let run once it is in its job: os/exec cannot put a process
// in a job as it is created. Command's processes are not, as a caller
// that started one without Runner.Start would leave it suspended for good.
// Nothing is left to do once Wait has returned: ending the job ends all of
// it at once.
func watchGroup(cmd *exec.Cmd) (waited func()) {
	if v, ok := groups.Load(cmd); ok {
		v.(*group).suspended = true
		cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	}
	return func() {}
}

// killOnClose has job end, or no longer end, when its last handle closes.
func killOnClose(job windows.Handle, on bool) {
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if on {
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	}
	windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
}

// resume lets every thread of process pid run: the one thread of a
// process created suspended.
func resume(pid uint32) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snap)
	te := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	found := false
	for e := windows.Thread32First(snap, &te); e == nil; e = windows.Thread32Next(snap, &te) {
		if te.OwnerProcessID != pid {
			continue
		}
		th, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, te.ThreadID)
		if err != nil {
			return err
		}
		_, err = windows.ResumeThread(th)
		windows.CloseHandle(th)
		if err != nil {
			return err
		}
		found = true
	}
	if !found {
		return errors.New("run: no thread to resume")
	}
	return nil
}

// leaveGroup forgets the group of a command that did not start.
func leaveGroup(cmd *exec.Cmd) { groups.Delete(cmd) }
