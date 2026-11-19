package ui

import (
	"testing"
	"time"
)

// Continue, once it ended well with the rebase stopped again, is offered
// again as soon as the state read after it shows, whatever order the end
// of the write and that read reach the main thread in: a callback told
// of the write's end after the read had come kept the bar off until some
// other change.
func TestOperationBarWakesAfterTheRead(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	for _, s := range []string{"1", "2", "3"} {
		r.Write("f.txt", s+"\n")
		r.Commit("step " + s)
	}
	// Each commit edited: a continue stops at the next, and git exits 0.
	r.Git("-c", "sequence.editor=sed -i.bak s/^pick/edit/", "rebase", "-q", "-i", "HEAD~3")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	tb := h.app.current()
	h.until("Continue offered", func() bool { b, ok := h.tt.Access("Continue"); return ok && !b.Disabled })
	logged := len(tb.s.Log)
	h.click("Continue")
	h.tt.Frame()
	if b, ok := h.tt.Access("Continue"); !ok || !b.Disabled {
		t.Fatal("Continue offered again while the first runs")
	}

	// Update by update: Continue is off until the state read after the
	// write's end shows, and on from that frame, with no other change
	// needed.
	e := tb.s.Log[logged]
	read := func() bool { return !e.Running && !tb.s.State.Refreshed.Before(e.Ended) }
	deadline := time.Now().Add(20 * time.Second)
	for {
		select {
		case fn := <-h.updates:
			fn()
		case <-time.After(20 * time.Millisecond):
		}
		h.tt.Frame()
		b, ok := h.tt.Access("Continue")
		if read() {
			if !ok || b.Disabled {
				t.Fatal("Continue stays off once the state read after the continue shows")
			}
			break
		}
		if ok && !b.Disabled {
			t.Fatal("Continue offered again before the state read after the continue")
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the state read after the continue")
		}
	}
	if e.Err != nil {
		t.Fatalf("continue: %v", e.Err)
	}
	if !h.tt.HasText("Rebase stopped") {
		t.Fatalf("the rebase did not stop on the next commit: %q", h.tt.Texts())
	}
}
