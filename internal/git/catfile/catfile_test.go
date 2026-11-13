package catfile

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

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

func decode(s string) []byte {
	b, _ := hex.DecodeString(s)
	return b
}
