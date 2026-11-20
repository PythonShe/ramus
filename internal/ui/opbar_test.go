package ui

import (
	"strings"
	"testing"
	"time"

	"ramus/internal/git/repo"
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

// A Skip that stops at the next commit's conflict fails, as git exits 1
// then: the bar offers nothing until the state read after it shows, or
// a second click would skip a commit whose conflict was never shown.
func TestOperationBarWaitsForTheReadAfterAFailedStep(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("f.txt", "0\n")
	r.Commit("base")
	r.Git("switch", "-q", "-c", "topic")
	for _, s := range []string{"A", "B", "C"} {
		r.Write("f.txt", s+"\n")
		r.Commit("commit " + s)
	}
	r.Git("switch", "-q", "main")
	r.Write("f.txt", "main\n")
	r.Commit("on main")
	r.Git("switch", "-q", "topic")
	r.Run(nil, "rebase", "main")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	tb := h.app.current()
	h.until("Skip offered", func() bool { b, ok := h.tt.Access("Skip"); return ok && !b.Disabled })
	stopped := func() string { return strings.TrimSpace(r.Git("log", "-1", "--format=%s", "REBASE_HEAD")) }
	if got := stopped(); got != "commit A" {
		t.Fatalf("stopped at %q", got)
	}
	logged := len(tb.s.Log)
	h.click("Skip")
	h.tt.Frame()
	e := tb.s.Log[logged]
	deadline := time.Now().Add(20 * time.Second)
	for {
		select {
		case fn := <-h.updates:
			fn()
		case <-time.After(20 * time.Millisecond):
		}
		h.tt.Frame()
		b, ok := h.tt.Access("Skip")
		if !e.Running && !tb.s.State.Refreshed.Before(e.Ended) {
			if !ok || b.Disabled {
				t.Fatal("Skip stays off once the state read after the skip shows")
			}
			break
		}
		if ok && !b.Disabled {
			t.Fatalf("Skip offered before the state read after the skip (ended %v, err %v) while git is stopped at %q", !e.Running, e.Err, stopped())
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the state read after the skip")
		}
	}
	if e.Err == nil {
		t.Fatal("the skip that stopped at a conflict succeeded")
	}
	if got := stopped(); got != "commit B" || len(tb.s.Log) != logged+1 {
		t.Fatalf("stopped at %q after %d writes", got, len(tb.s.Log)-logged)
	}
}

// Abort confirmed after the merge ended in a terminal does nothing when
// the app has seen it end, and tells of no failure when it had not: the
// bar sees the operation is over.
func TestAbortAfterTheOperationEnded(t *testing.T) {
	h := newHarness(t)
	r := conflictingRepo(h)
	r.Run(nil, "merge", "topic")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	tb := h.app.current()
	h.until("the bar", h.shows("Merge stopped"))

	// Seen to end.
	h.click("Abort…")
	h.until("the question", h.shows("Abort the merge?"))
	r.Git("merge", "--abort")
	h.until("the merge seen to end", func() bool { return tb.s.State.Operation == repo.NoOperation })
	logged := len(tb.s.Log)
	h.click("Abort")
	h.tt.Frame()
	if len(tb.s.Log) != logged {
		t.Fatalf("ran %q with no merge to abort", tb.s.Log[len(tb.s.Log)-1].Command)
	}

	// Not seen to end yet: git refuses, and that is no failure to tell.
	r.Run(nil, "merge", "topic")
	h.until("the bar again", h.shows("Merge stopped"))
	h.click("Abort…")
	h.until("the question again", h.shows("Abort the merge?"))
	r.Git("merge", "--abort")
	// No update is handed over before the click: the app still has the
	// merge going.
	h.click("Abort")
	if len(tb.s.Log) == logged {
		t.Fatal("Abort ran nothing")
	}
	e := tb.s.Log[len(tb.s.Log)-1]
	told := false
	h.until("the state read after the abort", func() bool {
		told = told || failedWrite(tb) != nil
		return !e.Running && !tb.s.State.Refreshed.Before(e.Ended) && tb.s.State.Operation == repo.NoOperation
	})
	h.tt.Frame()
	if e.Err == nil {
		t.Fatal("git aborted a merge that had ended")
	}
	if told || failedWrite(tb) != nil {
		t.Fatalf("told of %v", e.Err)
	}
}
