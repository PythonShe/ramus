package conflict

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/run"
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
		data, err := mergeFileOut(ctx, r, paths, append(style, "-L", "HEAD", "-L", "base", "-L", "topic")...)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(data, "\n=======\n") && !strings.HasPrefix(data, "=======\n") {
			continue // no conflict, as far as can be told cheaply
		}
		var sides mergedSides
		for i, p := range []*[]byte{&sides.Ours, &sides.Base, &sides.Theirs} {
			if *p, err = os.ReadFile(paths[i]); err != nil {
				t.Fatal(err)
			}
		}
		segs, err := remerge(ctx, r, []byte(data), defaultMarkerSize, sides)
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
			want, werr := mergeFileOut(ctx, r, paths, side.flag)
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

// A file near MaxSize with many conflicts, whose sides hold text like
// markers, is read: the merges of the sides again write no labels, which
// would make them longer than the file, and are run only in the style the
// file is in.
func TestRemergeNearTheSizeLimit(t *testing.T) {
	r := testrun.Runner(t)
	ctx := context.Background()
	dir := t.TempDir()
	var base, ours, theirs bytes.Buffer
	for i := 0; base.Len() < MaxSize-3<<20; i++ {
		// Underlines and examples, which histogram diffs fast around.
		line := fmt.Sprintf("line %d of the document with some text\n", i)
		switch {
		case i%50 == 7:
			line = "=======\n"
		case i%500 == 9:
			line = example
		}
		base.WriteString(line)
		if i%40 == 0 {
			fmt.Fprintf(&ours, "line %d changed by us\n", i)
			fmt.Fprintf(&theirs, "line %d changed by them\n", i)
			continue
		}
		ours.WriteString(line)
		theirs.WriteString(line)
	}
	var paths [3]string
	var sides mergedSides
	for i, b := range []*bytes.Buffer{&ours, &base, &theirs} {
		paths[i] = filepath.Join(dir, strconv.Itoa(i))
		if err := os.WriteFile(paths[i], b.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sides.Ours, sides.Base, sides.Theirs = ours.Bytes(), base.Bytes(), theirs.Bytes()
	data, err := mergeFileOut(ctx, r, paths, "-L", "HEAD", "-L", "base", "-L", "topic")
	if err != nil || len(data) > MaxSize {
		t.Fatalf("%d bytes, %v", len(data), err)
	}
	start := time.Now()
	segs, err := remerge(ctx, r, []byte(data), defaultMarkerSize, sides)
	t.Logf("%.1f MB, %d conflicts, read in %v", float64(len(data))/1e6, Count(segs), time.Since(start).Round(time.Millisecond))
	if err != nil || Count(segs) < 17500 {
		t.Fatalf("%d conflicts, %v", Count(segs), err)
	}
	cs := make([]Choice, Count(segs))
	for i := range cs {
		cs[i] = Ours
	}
	got, err := Resolve(segs, cs, nil)
	want, werr := mergeFileOut(ctx, r, paths, "--ours")
	if err != nil || werr != nil || string(got) != want {
		t.Fatalf("taking ours differs: %v %v", err, werr)
	}
}

// Reading a file stops when it is left, and past loadTime it is refused,
// with the way out a file that cannot be told apart has.
func TestLoadStops(t *testing.T) {
	lines := "a\nb\nc\nd\ne\nf\ng\nh\n"
	m := realMerge{base: example + lines + "end\n", ours: example + lines + "end ours\n", theirs: example + lines + "end theirs\n"}
	r, _, _, _ := m.run(t, "merge", "merge")
	if r == nil {
		t.Fatal("no conflict in the file")
	}
	data, err := os.ReadFile(filepath.Join(r.Dir, realFile))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Load(ctx, testrun.Runner(t), r.GitDir(), r.Dir, realFile, data); !errors.Is(err, context.Canceled) {
		t.Errorf("left: %v", err)
	}
	defer func(d time.Duration) { loadTime = d }(loadTime)
	loadTime = time.Nanosecond
	if _, err := Load(context.Background(), testrun.Runner(t), r.GitDir(), r.Dir, realFile, data); !errors.Is(err, ErrAmbiguous) {
		t.Errorf("past loadTime: %v", err)
	}
}

// mergeFileOut is what git merge-file writes for paths with markers of the
// default size and extra options.
func mergeFileOut(ctx context.Context, r *run.Runner, paths [3]string, extra ...string) (string, error) {
	var out bytes.Buffer
	err := runMergeFile(ctx, r, paths, defaultMarkerSize, &out, MaxSize, extra...)
	return out.String(), err
}
