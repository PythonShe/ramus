//go:build windows

package run

import (
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

// lower leaves the priority as it is: Windows has no nice.
func lower(cmd *exec.Cmd) {}

// stopGroup makes cancelling stop git and everything it started, as a
// hook's shell and what that runs, as a process group does elsewhere:
// killing git alone leaves its children running, holding the pipes the
// app reads until WaitDelay and the folders they run in.
func stopGroup(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Taken while git runs, so its children still name it as their
		// parent.
		tree := descendants(uint32(cmd.Process.Pid))
		err := cmd.Process.Kill()
		for _, pid := range tree {
			if h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid); err == nil {
				windows.TerminateProcess(h, 1)
				windows.CloseHandle(h)
			}
		}
		return err
	}
}

// descendants returns the processes pid started, and those they started,
// from a snapshot of the processes running.
func descendants(pid uint32) []uint32 {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)
	children := map[uint32][]uint32{}
	e := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if e.ProcessID != e.ParentProcessID {
			children[e.ParentProcessID] = append(children[e.ParentProcessID], e.ProcessID)
		}
	}
	// A parent's ID can be reused by a process started after it ended:
	// each process is taken once.
	seen := map[uint32]bool{pid: true}
	var out []uint32
	for next := []uint32{pid}; len(next) > 0; {
		p := next[0]
		next = next[1:]
		for _, c := range children[p] {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
				next = append(next, c)
			}
		}
	}
	return out
}
