package lifetime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/run"
)

type closer struct{ n *atomic.Int32 }

func (c closer) Close() error { c.n.Add(1); return nil }

func TestClose(t *testing.T) {
	before := runtime.NumGoroutine()
	l := New(context.Background())
	var stopped atomic.Bool
	var closedBeforeStop atomic.Bool
	var closes atomic.Int32
	l.Own(closer{&closes})
	release, _ := l.Own(closer{&closes})
	release()
	release()
	hold := make(chan struct{})
	l.Go(func(ctx context.Context) {
		<-ctx.Done()
		<-hold
		if closes.Load() > 1 {
			closedBeforeStop.Store(true)
		}
		stopped.Store(true)
	})
	path := filepath.Join(t.TempDir(), "f")
	os.WriteFile(path, []byte("mapped"), 0o644)
	m, err := l.Map(path)
	if err != nil || string(m.Bytes()) != "mapped" {
		t.Fatal(err)
	}
	// Close returns while the work it stopped still runs, held here.
	returned := make(chan struct{})
	go func() { l.Close(); close(returned) }()
	select {
	case <-returned:
	case <-time.After(time.Minute):
		t.Fatal("Close waited")
	}
	close(hold)
	<-l.Done()
	if !stopped.Load() || closedBeforeStop.Load() || closes.Load() != 2 {
		t.Fatalf("stopped %v, closed early %v, closes %d", stopped.Load(), closedBeforeStop.Load(), closes.Load())
	}
	if err := l.Go(func(context.Context) {}); err != ErrClosed {
		t.Error("Go after Close")
	}
	if _, err := l.Map(path); err != ErrClosed {
		t.Error("Map after Close")
	}
	l.Close()
	// Goroutines end a while after they are done with, longer on a busy
	// machine.
	for deadline := time.Now().Add(time.Minute); runtime.NumGoroutine() > before; {
		if time.Now().After(deadline) {
			t.Errorf("%d goroutines left, %d before", runtime.NumGoroutine(), before)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Closing stops the repository's git processes.
func TestCloseStopsSubprocesses(t *testing.T) {
	r := gittest.Init(t)
	runner := testrun.Runner(t)
	l := New(context.Background())
	started := make(chan struct{})
	l.Go(func(ctx context.Context) {
		close(started)
		runner.Run(ctx, &run.Cmd{GitDir: r.GitDir(), Args: []string{"cat-file", "--batch"}, Stdin: blockingReader{ctx}})
	})
	<-started
	deadline := time.Now().Add(time.Minute)
	for runner.Running() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	l.Close()
	select {
	case <-l.Done():
	case <-time.After(time.Minute):
		t.Fatal("close did not finish")
	}
	if runner.Running() != 0 {
		t.Errorf("%d processes alive", runner.Running())
	}
}

type blockingReader struct{ ctx context.Context }

func (b blockingReader) Read([]byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func TestPanicRecovered(t *testing.T) {
	l := New(context.Background())
	got := make(chan Panic, 2)
	l.OnPanic(func(p Panic) { got <- p })
	l.Go(func(context.Context) { panicInWork() })
	p := <-got
	if p.Value != "broken: 7" || !strings.Contains(p.Stack, "panicInWork") {
		t.Fatalf("%+v", p)
	}
	// The lifetime still ends.
	l.Close()
	select {
	case <-l.Done():
	case <-time.After(time.Minute):
		t.Fatal("not closed")
	}
}

func panicInWork() { panic(fmt.Sprintf("broken: %d", 7)) }
