package catfile

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

func TestRead(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a.txt", "hello\n")
	c := r.Commit("first")
	blob := strings.TrimSpace(r.Git("rev-parse", "HEAD:a.txt"))
	p, err := Start(context.Background(), testrun.Runner(t), r.GitDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ids := [][]byte{decode(c), decode(blob), decode(strings.Repeat("ab", 20)), decode(c)}
	var got []string
	err = p.Read(ids, func(i int, o Object) error {
		if o.Missing {
			got = append(got, "missing")
		} else {
			got = append(got, o.Type+":"+strings.SplitN(string(o.Data), "\n", 2)[0])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"commit:tree " + strings.TrimSpace(r.Git("rev-parse", "HEAD^{tree}")), "blob:hello", "missing", got[0]}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	// A callback error stops nothing but is returned, and the process
	// stays usable.
	stop := errors.New("stop")
	if err := p.Read(ids, func(int, Object) error { return stop }); err != stop {
		t.Fatalf("got %v", err)
	}
	if err := p.Read(ids[:1], func(int, Object) error { return nil }); err != nil {
		t.Fatal(err)
	}
	p.Close()
	if err := p.Read(ids[:1], func(int, Object) error { return nil }); !errors.Is(err, ErrClosed) {
		t.Fatalf("after Close: %v", err)
	}
}

// A process that dies says what it wrote to stderr, which exec copies
// from a goroutine of its own: run with -race.
func TestDiedSaysWhy(t *testing.T) {
	r := gittest.Init(t)
	c := r.Commit("first")
	// A loose object git cannot inflate, which it complains of on stderr.
	bad := strings.Repeat("ab", 20)
	if err := os.MkdirAll(filepath.Join(r.GitDir(), "objects", bad[:2]), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.GitDir(), "objects", bad[:2], bad[2:]), []byte("not zlib"), 0o444); err != nil {
		t.Fatal(err)
	}
	p, err := Start(context.Background(), testrun.Runner(t), r.GitDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.Read([][]byte{decode(bad)}, func(int, Object) error { return nil })
	if err := p.g.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := p.Read([][]byte{decode(c)}, func(int, Object) error { return nil }); !errors.Is(err, ErrClosed) {
		t.Fatalf("read once killed: %v", err)
	}
	if p.Alive() {
		t.Fatal("alive once killed")
	}
}

// git ends once it has had nothing to read for a while, letting go of
// the packs it holds open, and starts again for the next read. It never
// runs inside the repository, which Windows would then not let move.
func TestEndsWhenIdle(t *testing.T) {
	r := gittest.Init(t)
	c := r.Commit("first")
	runner := testrun.Runner(t)
	p, err := Start(context.Background(), runner, r.GitDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if dir, _ := filepath.Rel(r.Dir, p.g.cmd.Dir); !strings.HasPrefix(dir, "..") {
		t.Fatalf("git runs in %s, inside the repository", p.g.cmd.Dir)
	}
	p.mu.Lock()
	p.idle = 20 * time.Millisecond
	p.mu.Unlock()
	read := func() {
		t.Helper()
		var typ string
		if err := p.Read([][]byte{decode(c)}, func(_ int, o Object) error { typ = o.Type; return nil }); err != nil || typ != "commit" {
			t.Fatalf("read %q, %v", typ, err)
		}
	}
	for range 2 {
		read()
		deadline := time.Now().Add(10 * time.Second)
		for runner.Running() != 0 {
			if time.Now().After(deadline) {
				t.Fatal("git still runs while idle")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !p.Alive() {
			t.Fatal("not alive once idle")
		}
	}
	read()
	if runner.Running() != 1 {
		t.Fatalf("%d gits running", runner.Running())
	}
}

// An id that is not whole is answered missing, not sent: git would end
// the batch on an empty one and take a short one for an abbreviation of
// whatever object it prefixes.
func TestPartialIDsAreMissing(t *testing.T) {
	r := gittest.Init(t)
	c := decode(r.Commit("first"))
	p, err := Start(context.Background(), testrun.Runner(t), r.GitDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	var got []string
	err = p.Read([][]byte{nil, c[:4], c, c[:19]}, func(i int, o Object) error {
		got = append(got, fmt.Sprintf("%v %s", o.Missing, o.Type))
		return nil
	})
	if want := "true |true |false commit|true "; err != nil || strings.Join(got, "|") != want {
		t.Fatalf("got %q, %v, want %q", got, err, want)
	}
	if !p.Alive() {
		t.Fatal("not alive")
	}
}

// The buffer of a large object is let go of once its batch is over.
func TestLargeBufferNotKept(t *testing.T) {
	r := gittest.Init(t)
	r.Write("big", strings.Repeat("x", 4<<20))
	r.Write("small", "x\n")
	r.Commit("first")
	big := decode(strings.TrimSpace(r.Git("rev-parse", "HEAD:big")))
	small := decode(strings.TrimSpace(r.Git("rev-parse", "HEAD:small")))
	p, err := Start(context.Background(), testrun.Runner(t), r.GitDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	size := 0
	if err := p.Read([][]byte{big}, func(_ int, o Object) error { size = len(o.Data); return nil }); err != nil || size != 4<<20 {
		t.Fatalf("read %d bytes, %v", size, err)
	}
	if cap(p.buf) > keepBuf {
		t.Fatalf("kept a buffer of %d bytes", cap(p.buf))
	}
	if err := p.Read([][]byte{small}, func(int, Object) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if cap(p.buf) == 0 {
		t.Fatal("a small buffer is not kept")
	}
}

// A batch in progress keeps neither Alive nor another read's cancel
// waiting, and cancelling it ends the batch, not the process.
func TestCancel(t *testing.T) {
	r := gittest.Init(t)
	c := r.Commit("first")
	p, err := Start(context.Background(), testrun.Runner(t), r.GitDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ids := [][]byte{decode(c), decode(c)}
	ctx, cancel := context.WithCancel(context.Background())
	inside, release := make(chan struct{}), make(chan struct{})
	done := make(chan error)
	go func() {
		done <- p.ReadContext(ctx, ids, func(i int, _ Object) error {
			if i == 0 {
				close(inside)
				<-release
			}
			return nil
		})
	}()
	<-inside
	within := func(what string, f func()) {
		t.Helper()
		ok := make(chan struct{})
		go func() { f(); close(ok) }()
		select {
		case <-ok:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s waits for the batch", what)
		}
	}
	within("Alive", func() {
		if !p.Alive() {
			t.Error("not alive during a batch")
		}
	})
	waiting, stop := context.WithCancel(context.Background())
	within("a cancelled read waiting its turn", func() {
		go stop()
		if err := p.ReadContext(waiting, ids, func(int, Object) error { return nil }); !errors.Is(err, context.Canceled) {
			t.Errorf("waiting read: %v", err)
		}
	})
	cancel()
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled batch: %v", err)
	}
	if !p.Alive() {
		t.Fatal("a cancelled batch ended the process")
	}
	n := 0
	if err := p.Read(ids, func(_ int, o Object) error { n++; return nil }); err != nil || n != 2 {
		t.Fatalf("read after a cancel: %d, %v", n, err)
	}
}

// Close does not wait for a batch blocked in its callback.
func TestCloseDuringABatch(t *testing.T) {
	r := gittest.Init(t)
	c := r.Commit("first")
	p, err := Start(context.Background(), testrun.Runner(t), r.GitDir())
	if err != nil {
		t.Fatal(err)
	}
	inside, release := make(chan struct{}), make(chan struct{})
	done := make(chan error)
	go func() {
		done <- p.Read([][]byte{decode(c), decode(c)}, func(i int, _ Object) error {
			if i == 0 {
				close(inside)
				<-release
			}
			return nil
		})
	}()
	<-inside
	closed := make(chan struct{})
	go func() { p.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close waits for the batch")
	}
	close(release)
	<-done
	if err := p.Read([][]byte{decode(c)}, func(int, Object) error { return nil }); !errors.Is(err, ErrClosed) {
		t.Fatalf("read after Close: %v", err)
	}
}

func decode(s string) []byte {
	b, _ := hex.DecodeString(s)
	return b
}

// A callback that panics mid-batch ends the process, which then closes
// rather than waiting on git blocked writing the answers no one reads,
// and reads nothing more: its answers would be the old batch's.
func TestPanicEndsTheProcess(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a.txt", strings.Repeat("x", 1<<20))
	c := r.Commit("first")
	blob := strings.TrimSpace(r.Git("rev-parse", "HEAD:a.txt"))
	p, err := Start(context.Background(), testrun.Runner(t), r.GitDir())
	if err != nil {
		t.Fatal(err)
	}
	ids := [][]byte{decode(c)}
	for range 64 {
		ids = append(ids, decode(blob))
	}
	func() {
		defer func() { recover() }()
		p.Read(ids, func(int, Object) error { panic("boom") })
	}()
	if p.Alive() {
		t.Fatal("alive after an abandoned batch")
	}
	done := make(chan struct{})
	go func() { p.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Close hangs")
	}
}
