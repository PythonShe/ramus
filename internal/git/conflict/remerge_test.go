package conflict

import (
	"context"
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/gittest/testrun"
)

// Files whose sides hold lines like markers of every kind, merged by git
// merge-file in every style, are read as git merged them, or refused: git
// is the oracle, and taking every conflict by one side gives what git
// merge-file --ours or --theirs writes, so no line is dropped or added.
// The cases are random from a fixed seed, as many as fit in the time.
func TestRemergeAgainstGit(t *testing.T) {
	r := testrun.Runner(t)
	ctx := context.Background()
	dir := t.TempDir()
	rng := rand.New(rand.NewPCG(1, 2))
	marks := []string{"=======", "||||||| x", "||||||| base", "<<<<<<< HEAD", "<<<<<<< x", ">>>>>>> topic", ">>>>>>> x"}
	block := "<<<<<<< HEAD\nyour change\n=======\ntheir change\n>>>>>>> topic"
	pick := func() string {
		switch n := rng.IntN(10); {
		case n < 3:
			return marks[rng.IntN(len(marks))]
		case n == 3:
			return block
		default:
			return string(rune('a' + rng.IntN(6)))
		}
	}
	edit := func(lines []string) []string {
		out := append([]string(nil), lines...)
		for range 1 + rng.IntN(3) {
			i := rng.IntN(len(out) + 1)
			switch rng.IntN(3) {
			case 0:
				out = append(out[:i], append([]string{pick()}, out[i:]...)...)
			case 1:
				if i < len(out) {
					out[i] = pick()
				}
			default:
				if i < len(out) {
					out = append(out[:i], out[i+1:]...)
				}
			}
		}
		return out
	}
	write := func(name string, lines []string) string {
		p := filepath.Join(dir, name)
		text := strings.Join(lines, "\n")
		if len(lines) > 0 {
			text += "\n"
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	limit := 8 * time.Second
	if testing.Short() {
		limit = 2 * time.Second
	}
	start := time.Now()
	read, refused, trials := 0, 0, 0
	for time.Since(start) < limit && trials < 600 {
		trials++
		base := make([]string, 4+rng.IntN(6))
		for i := range base {
			base[i] = pick()
		}
		ours, theirs := edit(base), edit(base)
		paths := [3]string{write("ours", ours), write("base", base), write("theirs", theirs)}
		var style []string
		if s := rng.IntN(3); s > 0 {
			style = []string{[]string{"", "--diff3", "--zdiff3"}[s]}
		}
		data, err := runMergeFile(ctx, r, paths, DefaultMarkerSize, append(style, "-L", "HEAD", "-L", "base", "-L", "topic")...)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(data, "\n=======\n") && !strings.HasPrefix(data, "=======\n") {
			continue // no conflict, as far as can be told cheaply
		}
		var sides Sides
		for i, p := range []*[]byte{&sides.Ours, &sides.Base, &sides.Theirs} {
			if *p, err = os.ReadFile(paths[i]); err != nil {
				t.Fatal(err)
			}
		}
		segs, err := remerge(ctx, r, []byte(data), DefaultMarkerSize, sides)
		if errors.Is(err, ErrAmbiguous) {
			refused++
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		read++
		for _, side := range []struct {
			flag   string
			choice Choice
		}{{"--ours", Ours}, {"--theirs", Theirs}} {
			cs := make([]Choice, Count(segs))
			for i := range cs {
				cs[i] = side.choice
			}
			got, err := Resolve(segs, cs, nil)
			want, werr := runMergeFile(ctx, r, paths, DefaultMarkerSize, side.flag)
			if err != nil || werr != nil || string(got) != want {
				t.Fatalf("trial %d %v, %s: %v %v\ngot\n%s\nwant\n%s\nmerged\n%s", trials, style, side.flag, err, werr, got, want, data)
			}
		}
	}
	t.Logf("%d trials: %d read, %d refused", trials, read, refused)
	if read < refused {
		t.Errorf("%d read, %d refused: most merges should be read", read, refused)
	}
}
