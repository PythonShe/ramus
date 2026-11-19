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
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var comspec = filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")

// A process started suspended is in its job before it runs, and runs.
func TestJobHoldsTheProcessFromTheStart(t *testing.T) {
	var out bytes.Buffer
	cmd := exec.Command(comspec, "/c", "echo answered")
	cmd.Stdout = &out
	j := InJob(cmd, false)
	j.Suspend(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := j.Join(cmd); err != nil {
		t.Fatal(err)
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(cmd.Process.Pid))
	if err == nil {
		var in int32
		isProcessInJob := windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")
		if ok, _, err := isProcessInJob.Call(uintptr(h), 0, uintptr(unsafe.Pointer(&in))); ok == 0 {
			t.Error(err)
		} else if in == 0 {
			t.Error("not in a job")
		}
		windows.CloseHandle(h)
	}
	if err := cmd.Wait(); err != nil || !bytes.Contains(out.Bytes(), []byte("answered")) {
		t.Fatalf("%v: %q", err, out.String())
	}
}

// Where no job could be had, cancelling still ends what the process
// started, which would otherwise hold its output open until WaitDelay.
func TestNoJobEndsTheTree(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, comspec, "/c", "ping -n 60 127.0.0.1")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.WaitDelay = 30 * time.Second
	j := InJob(cmd, false)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	j.fallBack(uint32(cmd.Process.Pid))
	// The ping has started once it has printed a line with its target.
	sc := bufio.NewScanner(out)
	for sc.Scan() && !strings.Contains(sc.Text(), "127.0.0.1") {
	}
	start := time.Now()
	cancel()
	io.Copy(io.Discard, out)
	cmd.Wait()
	if d := time.Since(start); d > 15*time.Second {
		t.Fatalf("ended after %v: what the process started kept running", d)
	}
}
