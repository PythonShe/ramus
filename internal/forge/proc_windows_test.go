//go:build windows

package forge

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// gh and glab start with no console window: the app has no console, and
// each would otherwise open one of its own, over the window typed in.
func TestToolHasNoWindow(t *testing.T) {
	cmd := exec.Command("gh", "api")
	ownGroup(cmd)
	a := cmd.SysProcAttr
	if a == nil || !a.HideWindow || a.CreationFlags&windows.CREATE_NO_WINDOW == 0 || cmd.Cancel == nil {
		t.Fatalf("attributes %+v", a)
	}
}

// A tool run in its job answers, and one cancelled is ended with what
// it started, which would otherwise hold its output open.
func TestToolInAJob(t *testing.T) {
	comspec := filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	var out bytes.Buffer
	cmd := exec.Command(comspec, "/c", "echo answered")
	cmd.Stdout = &out
	if err := runTool(cmd); err != nil || !strings.Contains(out.String(), "answered") {
		t.Fatalf("%v: %q", err, out.String())
	}

	// The ping that outlives cmd starts a second in, well after the job
	// is joined.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out.Reset()
	cmd = exec.CommandContext(ctx, comspec, "/c", "ping -n 2 127.0.0.1 >nul & ping -n 60 127.0.0.1")
	cmd.Stdout = &out
	cmd.WaitDelay = 30 * time.Second
	start := time.Now()
	runTool(cmd)
	if d := time.Since(start); d > 15*time.Second {
		t.Fatalf("cancelled after %v: what the tool started kept running", d)
	}
}
