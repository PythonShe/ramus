package write

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/refs"
	"ramus/internal/git/run"
)

// Thousands of paths stage, unstage and discard in one git process each,
// the paths on its standard input, never on its command line.
func TestManyPathsOneProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("writes 5,000 files")
	}
	r, q, _ := newQueue(t)
	q.Refs = refs.New(r.GitDir(), r.GitDir(), 20)
	w := Where{GitDir: r.GitDir(), WorkTree: r.Dir}
	const n = 5000
	var paths []string
	for i := range n {
		// Names a shell or a pathspec would read otherwise.
		p := fmt.Sprintf("d%02d/f %d *[x].txt", i%50, i)
		r.Write(p, "one\n")
		paths = append(paths, p)
	}
	r.Commit("files")
	for _, p := range paths {
		r.Write(p, "two\n")
	}
	count := func(args ...string) int {
		return strings.Count(r.Git(append(args, "-z")...), "\x00")
	}
	for _, step := range []struct {
		name string
		op   Op
		ok   func() bool
	}{
		{"stage", Stage(w, paths), func() bool { return count("diff", "--cached", "--name-only") == n }},
		{"unstage", Unstage(w, paths, false), func() bool { return count("diff", "--cached", "--name-only") == 0 }},
		{"discard", Discard(w, paths), func() bool { return count("diff", "--name-only") == 0 }},
	} {
		if len(step.op.Cmd.Args) > 8 || !strings.Contains(strings.Join(step.op.Cmd.Args, " "), "--pathspec-from-file=-") {
			t.Fatalf("%s passes %d arguments: %q", step.name, len(step.op.Cmd.Args), step.op.Cmd.Args[:min(8, len(step.op.Cmd.Args))])
		}
		start := time.Now()
		j := q.Submit(step.op)
		if _, err := wait(t, j); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		t.Logf("%s %d files: %v (snapshot included for discard)", step.name, n, time.Since(start).Round(time.Millisecond))
		if !step.ok() {
			t.Fatalf("%s left the files as they were", step.name)
		}
	}

	// git clean reads no paths from standard input: deleting thousands
	// of untracked files is one write of a few runs, each under the
	// Windows command line limit.
	var untracked []string
	for i := range n {
		p := fmt.Sprintf("new/u%02d/g %d [y].txt", i%50, i)
		r.Write(p, "new\n")
		untracked = append(untracked, p)
	}
	op, err := Delete(w, untracked)
	if err != nil {
		t.Fatal(err)
	}
	runs := append([]run.Cmd{op.Cmd}, op.More...)
	for _, c := range runs {
		if l := len(strings.Join(c.Args, " ")); l > 32<<10 {
			t.Fatalf("a clean run is %d bytes long", l)
		}
	}
	start := time.Now()
	if _, err := wait(t, q.Submit(op)); err != nil {
		t.Fatal(err)
	}
	t.Logf("delete %d untracked files: %d runs, %v (snapshot included)", n, len(runs), time.Since(start).Round(time.Millisecond))
	if got := count("ls-files", "--others", "--exclude-standard"); got != 0 {
		t.Fatalf("%d untracked files left", got)
	}
	if _, err := wait(t, q.Undo()); err != nil {
		t.Fatal(err)
	}
	if got := count("ls-files", "--others", "--exclude-standard"); got != n {
		t.Fatalf("undo brought back %d of %d untracked files", got, n)
	}
}
