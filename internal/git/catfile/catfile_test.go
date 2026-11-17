package catfile

import (
	"context"
	"encoding/hex"
	"errors"
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
	if err := p.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := p.Read([][]byte{decode(c)}, func(int, Object) error { return nil }); !errors.Is(err, ErrClosed) {
		t.Fatalf("read once killed: %v", err)
	}
	if p.Alive() {
		t.Fatal("alive once killed")
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
