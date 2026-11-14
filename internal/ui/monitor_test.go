package ui

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/run"
	"ramus/internal/git/status"
)

// The offer shows the exact commands and runs them only when asked.
func TestMonitorOffer(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("open", h.shows("No changes"))
	tb := h.app.current()
	rp := tb.s.Repo
	a, ok := status.AdviseMonitor(runtime.GOOS, rp.Runner.Git().Version, rp.Config(), rp.GitDir, rp.WorkTree, true, 2*time.Second)
	if !ok {
		t.Skip("no offer on this system")
	}
	// Only the cache: a monitor turned on in a test would start a daemon
	// that outlives it.
	var keep []run.Cmd
	for _, c := range a.Cmds {
		if c.Args[1] != "core.fsmonitor" {
			keep = append(keep, c)
		}
	}
	a.Cmds, a.Daemon = keep, ""
	tb.s.State.Monitor = &a
	h.until("the offer", h.shows("git config core.untrackedCache true"))
	h.click("Turn On")
	tb2 := tb
	h.idle(tb2, "the config written")
	if got := strings.TrimSpace(r.Git("config", "core.untrackedCache")); got != "true" {
		t.Fatalf("core.untrackedCache %q", got)
	}
	if h.tt.HasText("Turn On") {
		t.Fatal("the offer stays after it was taken")
	}
	if out, _ := r.Run(nil, "config", "--get", "core.fsmonitor"); out != "" {
		t.Fatal("the monitor was turned on")
	}
}
