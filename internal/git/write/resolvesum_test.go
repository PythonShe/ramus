package write

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A merge saved over a conflicted file is refused when the file's bytes
// differ from those the user saw, though an edit kept its size and time.
func TestResolveRefusesAnEditOfTheSameSizeAndTime(t *testing.T) {
	h := newHarness(t)
	h.r.Write("a", "base\n")
	h.r.Commit("base")
	h.r.Git("checkout", "-q", "-b", "topic")
	h.r.Write("a", "theirs\n")
	h.r.Commit("theirs")
	h.r.Git("checkout", "-q", "main")
	h.r.Write("a", "ours\n")
	h.r.Commit("ours")
	h.r.Run(nil, "merge", "topic")
	p := filepath.Join(h.r.Dir, "a")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	seen, err := Observe(h.r.Dir, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	seen[0].Sum = ContentSum(data)
	edited := []byte(string(data[:len(data)-2]) + "X\n")
	if err := os.WriteFile(p, edited, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, seen[0].ModTime, seen[0].ModTime); err != nil {
		t.Fatal(err)
	}
	op, err := Resolve(h.w, "a", []byte("resolved\n"), seen[0])
	if err != nil {
		t.Fatal(err)
	}
	var se *StaleError
	if _, err := wait(t, h.q.Submit(op)); !errors.As(err, &se) {
		t.Fatalf("err %v", err)
	}
	if b, _ := os.ReadFile(p); string(b) != string(edited) {
		t.Fatalf("a %q", b)
	}

	// As seen, whatever its time: saved.
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	op, _ = Resolve(h.w, "a", []byte("resolved\n"), seen[0])
	if _, err := wait(t, h.q.Submit(op)); err != nil {
		t.Fatal(err)
	}
}
