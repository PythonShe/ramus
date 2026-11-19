package write

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/lifetime"
	"ramus/internal/git/run"
)

func newQueue(t *testing.T) (*gittest.Repo, *Queue, *lifetime.Lifetime) {
	t.Helper()
	r := gittest.Init(t)
	life := lifetime.New(context.Background())
	t.Cleanup(func() { life.Close(); <-life.Done() })
	q, err := NewQueue(life, testrun.Runner(t), r.GitDir())
	if err != nil {
		t.Fatal(err)
	}
	return r, q, life
}

func commitOp(r *gittest.Repo, msg string) Op {
	return Op{Name: "Commit", Cmd: run.Cmd{GitDir: r.GitDir(), WorkTree: r.Dir,
		Args: []string{"commit", "-q", "--allow-empty", "-m", msg}}}
}

func hook(t *testing.T, r *gittest.Repo, name, body string) {
	t.Helper()
	p := filepath.Join(r.GitDir(), "hooks", name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func wait(t *testing.T, j *Job) (run.Result, error) {
	t.Helper()
	select {
	case <-j.Done():
	case <-time.After(waitLimit(t)):
		t.Fatalf("%s did not end", j.Op.Name)
	}
	return j.Result()
}

// waitLimit is how long a test waits for a write: as long as go test lets
// it run, short of a minute to report which write did not end. A write
// over ten thousand files takes a minute on a loaded two-core runner.
func waitLimit(t *testing.T) time.Duration {
	if d, ok := t.Deadline(); ok {
		return max(time.Until(d)-time.Minute, 30*time.Second)
	}
	return 10 * time.Minute
}

func TestQueueRunsInOrderWithLiveOutput(t *testing.T) {
	r, q, _ := newQueue(t)
	hook(t, r, "pre-commit", `echo "checking $(git rev-list --count --all)" >&2`)
	var jobs []*Job
	for _, m := range []string{"one", "two", "three"} {
		jobs = append(jobs, q.Submit(commitOp(r, m)))
	}
	var seen []string
	for i, j := range jobs {
		for ev := range j.Events() {
			if ev.Stream == Stderr {
				seen = append(seen, ev.Line)
			}
		}
		if _, err := wait(t, j); err != nil {
			t.Fatalf("job %d: %v", i, err)
		}
	}
	if want := []string{"checking 0", "checking 1", "checking 2"}; !slices.Equal(seen, want) {
		t.Fatalf("hook output %q, want %q", seen, want)
	}
	if got := strings.Fields(r.Git("log", "--format=%s")); !slices.Equal(got, []string{"three", "two", "one"}) {
		t.Fatalf("log %q", got)
	}
	if q.Pending() != 0 {
		t.Fatalf("pending %d", q.Pending())
	}
}

func TestCancel(t *testing.T) {
	r, q, _ := newQueue(t)
	hook(t, r, "pre-commit", "echo started >&2; sleep 30")
	slow := q.Submit(commitOp(r, "slow"))
	queued := q.Submit(commitOp(r, "queued"))
	ev, ok := <-slow.Events()
	if !ok || ev.Line != "started" {
		t.Fatalf("first event %+v %v", ev, ok)
	}
	queued.Cancel()
	select {
	case <-queued.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled queued job did not end at once")
	}
	if _, err := queued.Result(); !errors.Is(err, context.Canceled) {
		t.Fatalf("queued: %v", err)
	}
	t0 := time.Now()
	slow.Cancel()
	if _, err := wait(t, slow); !errors.Is(err, context.Canceled) {
		t.Fatalf("running: %v", err)
	}
	if d := time.Since(t0); d > 5*time.Second {
		t.Fatalf("cancel took %v", d)
	}
	if out, err := r.Run(nil, "rev-parse", "-q", "--verify", "HEAD"); err == nil {
		t.Fatalf("a cancelled job committed %s", out)
	}
	hook(t, r, "pre-commit", "true")
	if _, err := wait(t, q.Submit(commitOp(r, "after"))); err != nil {
		t.Fatalf("queue stuck after cancel: %v", err)
	}
}

func TestClose(t *testing.T) {
	r, q, life := newQueue(t)
	hook(t, r, "pre-commit", "echo started >&2; sleep 30")
	running := q.Submit(commitOp(r, "running"))
	<-running.Events()
	queued := q.Submit(commitOp(r, "queued"))
	life.Close()
	if _, err := wait(t, running); err == nil {
		t.Fatal("running job survived close")
	}
	if _, err := wait(t, queued); err == nil {
		t.Fatal("queued job survived close")
	}
	<-life.Done()
	if _, err := wait(t, q.Submit(commitOp(r, "late"))); !errors.Is(err, lifetime.ErrClosed) {
		t.Fatalf("submit after close: %v", err)
	}
}

func TestLines(t *testing.T) {
	j := &Job{events: make(chan Event, 16)}
	l := &lines{j: j, stream: Stderr}
	l.Write([]byte("Receiving objects:  50% (1/2)\rReceiving obj"))
	l.Write([]byte("ects: 100% (2/2), done.\r\nremote: hello\n\npartial"))
	l.flush()
	close(j.events)
	var got []Event
	for ev := range j.events {
		got = append(got, ev)
	}
	want := []Event{
		{Stderr, "Receiving objects:  50% (1/2)", 50},
		{Stderr, "Receiving objects: 100% (2/2), done.", 100},
		{Stderr, "remote: hello", -1},
		{Stderr, "partial", -1},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("events\n%+v\nwant\n%+v", got, want)
	}

	full := &Job{events: make(chan Event, 1)}
	l = &lines{j: full, stream: Stdout}
	l.Write([]byte("a\nb\nc\n"))
	if full.dropped != 2 {
		t.Fatalf("dropped %d", full.dropped)
	}
}

func TestQueueGoesOnAfterAJobPanics(t *testing.T) {
	r, q, life := newQueue(t)
	heard := make(chan lifetime.Panic, 1)
	life.OnPanic(func(p lifetime.Panic) { heard <- p })
	bad := Op{Name: "Bad", do: func(context.Context, *Job) (run.Result, error) { panic("boom") }}
	if _, err := wait(t, q.Submit(bad)); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("a job that panicked ended with %v", err)
	}
	select {
	case p := <-heard:
		if p.Value != "boom" {
			t.Fatalf("heard %q", p.Value)
		}
	default:
		t.Fatal("the panic was not reported")
	}
	if _, err := wait(t, q.Submit(commitOp(r, "after"))); err != nil {
		t.Fatalf("the queue stopped after a panic: %v", err)
	}
}
