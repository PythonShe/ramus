package conflict

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

// randomMerges is a worker of TestLoadAgainstRealMerges: one repository,
// whose files merge with the attributes it gives.
type randomMerges struct {
	r *gittest.Repo
	// attrs is the base's and ours' .gitattributes, theirs theirs'.
	attrs, theirs string
	// sizes are the marker sizes the sides' text imitates.
	sizes []int
}

// realStats counts the trials of TestLoadAgainstRealMerges.
type realStats struct {
	mu sync.Mutex
	// conflicted are the merges that left the file conflicted; marked those
	// whose sides hold lines like markers.
	conflicted, read, refused, marked, markedRefused int
	// space counts the sides taken that differ from the oracle's in white
	// space only, in a merge that ignored it.
	space int
	wrong []string
}

// Files merged by real git merges, with every conflict style, diff
// algorithm and white space option, sides with lines like markers of the
// size merged with and of others, sides with no final newline (where git
// writes an open marker on a line when white space is ignored), CRLF in
// the work tree, marker sizes set, and a merged branch that changes the
// size: each is read as git merged it or refused, never read wrong. git
// is the oracle: taking every conflict by one side must give what git
// merges taking that side (merge-tree -X ours or theirs). The trials are
// random from a fixed seed, as many as fit in the time.
func TestLoadAgainstRealMerges(t *testing.T) {
	limit := 10 * time.Second
	if testing.Short() {
		limit = 3 * time.Second
	}
	size10 := realFile + " conflict-marker-size=10\n"
	workers := []randomMerges{
		{sizes: []int{7}},
		{sizes: []int{7}},
		{attrs: realFile + " conflict-marker-size=3\n", theirs: realFile + " conflict-marker-size=3\n", sizes: []int{3, 7}},
		{attrs: size10, theirs: size10, sizes: []int{10, 7}},
		{attrs: "* text eol=crlf\n", theirs: "* text eol=crlf\n", sizes: []int{7}},
		{theirs: size10, sizes: []int{7, 10}},
		{attrs: size10, sizes: []int{10, 7}},
		{attrs: "", theirs: "* text eol=crlf\n" + size10, sizes: []int{7, 10}},
	}
	st := &realStats{}
	deadline := time.Now().Add(limit)
	var wg sync.WaitGroup
	for i := range workers {
		w := &workers[i]
		w.r = gittest.Init(t)
		rng := rand.New(rand.NewPCG(7, uint64(i)))
		wg.Go(func() { w.loop(t, rng, deadline, st) })
	}
	wg.Wait()
	t.Logf("%d merges left the file conflicted: %d read, %d refused (%.1f%%); with lines like markers in the sides %d, %d refused (%.1f%%)",
		st.conflicted, st.read, st.refused, percent(st.refused, st.conflicted), st.marked, st.markedRefused, percent(st.markedRefused, st.marked))
	t.Logf("%d sides taken in merges that ignored white space differ from the oracle's in it alone", st.space)
	for i, w := range st.wrong {
		if i == 5 {
			break
		}
		t.Errorf("read wrong: %s", w)
	}
	if len(st.wrong) > 0 {
		t.Errorf("%d of %d read wrong", len(st.wrong), st.read)
	}
	if st.read < st.refused {
		t.Errorf("%d read, %d refused: most merges should be read", st.read, st.refused)
	}
}

func percent(n, of int) float64 {
	if of == 0 {
		return 0
	}
	return 100 * float64(n) / float64(of)
}

// realTrial is one merge: the file in the base and on each side, how git
// merges them, and the trial's branches.
type realTrial struct {
	id                 int
	base, ours, theirs string
	style              string
	x                  []string
}

func ignoresSpace(x []string) bool {
	return len(x) == 2 && strings.HasPrefix(x[1], "ignore-")
}

// sameBesideSpace reports whether a and b hold the same lines, white space
// in and around them aside.
func sameBesideSpace(a, b string) bool {
	lines := func(s string) []string {
		ls := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
		for i, l := range ls {
			ls[i] = strings.Join(strings.Fields(l), "")
		}
		return ls
	}
	return slices.Equal(lines(a), lines(b))
}

func (w *randomMerges) gen(rng *rand.Rand, id int) realTrial {
	m := func(c byte, n int) string { return strings.Repeat(string(c), n) }
	pick := func() string {
		s := w.sizes[rng.IntN(len(w.sizes))]
		switch n := rng.IntN(20); {
		case n < 4:
			return []string{m('=', s), m('<', s) + " HEAD", m('>', s) + " topic", m('|', s) + " base", m('<', s), m('>', s), m('|', s), m('=', s) + " x"}[rng.IntN(8)]
		case n < 6:
			return []string{"}", "", "---", "  }"}[rng.IntN(4)]
		case n == 6:
			return m('<', s) + " HEAD\nyour change\n" + m('=', s) + "\ntheir change\n" + m('>', s) + " topic"
		case n == 7:
			return []string{" a", "a ", "\tb", "b  ", m('=', s) + " "}[rng.IntN(5)]
		default:
			return string(rune('a' + rng.IntN(6)))
		}
	}
	plain := rng.IntN(5) == 0
	line := func() string {
		if plain {
			return string(rune('a' + rng.IntN(6)))
		}
		return pick()
	}
	edit := func(lines []string) []string {
		out := append([]string(nil), lines...)
		for range 1 + rng.IntN(4) {
			i := rng.IntN(len(out) + 1)
			switch rng.IntN(3) {
			case 0:
				out = append(out[:i], append([]string{line()}, out[i:]...)...)
			case 1:
				if i < len(out) {
					out[i] = line()
				}
			default:
				if i < len(out) {
					out = append(out[:i], out[i+1:]...)
				}
			}
		}
		return out
	}
	join := func(lines []string) string {
		s := strings.Join(lines, "\n")
		if len(lines) > 0 && rng.IntN(4) != 0 {
			s += "\n" // else no final newline
		}
		return s
	}
	base := make([]string, 4+rng.IntN(10))
	for i := range base {
		base[i] = line()
	}
	tr := realTrial{id: id, base: join(base), ours: join(edit(base)), theirs: join(edit(base))}
	tr.style = []string{"merge", "diff3", "zdiff3"}[rng.IntN(3)]
	switch x := []string{"", "", "", "myers", "patience", "minimal", "ignore-space-change", "ignore-all-space", "ignore-space-at-eol"}[rng.IntN(9)]; {
	case x == "":
	case strings.HasPrefix(x, "ignore"):
		tr.x = []string{"-X", x}
	default:
		tr.x = []string{"-X", "diff-algorithm=" + x}
	}
	return tr
}

func (w *randomMerges) loop(t *testing.T, rng *rand.Rand, deadline time.Time, st *realStats) {
	for id := 0; time.Now().Before(deadline); {
		// A batch of trials made at once, each a base commit and its sides.
		var trials []realTrial
		var fi bytes.Buffer
		data := func(s string) { fmt.Fprintf(&fi, "data %d\n%s\n", len(s), s) }
		for range 20 {
			id++
			tr := w.gen(rng, id)
			trials = append(trials, tr)
			for _, c := range []struct{ ref, file, attrs, from string }{
				{"b", tr.base, w.attrs, ""}, {"o", tr.ours, w.attrs, "b"}, {"t", tr.theirs, w.theirs, "b"},
			} {
				fmt.Fprintf(&fi, "commit refs/heads/%s%d\ncommitter T <t@example.com> 1700000000 +0000\n", c.ref, id)
				data(c.ref)
				if c.from != "" {
					fmt.Fprintf(&fi, "from refs/heads/%s%d\n", c.from, id)
				}
				fmt.Fprintf(&fi, "M 100644 inline %s\n", realFile)
				data(c.file)
				if c.attrs != "" {
					fmt.Fprintf(&fi, "M 100644 inline .gitattributes\n")
					data(c.attrs)
				} else if c.from != "" {
					fmt.Fprintf(&fi, "D .gitattributes\n")
				}
			}
		}
		if _, err := w.r.Run(fi.Bytes(), "fast-import", "--quiet"); err != nil {
			t.Errorf("fast-import: %v", err)
			return
		}
		for _, tr := range trials {
			if time.Now().After(deadline) {
				return
			}
			if err := w.trial(t, tr, st); err != nil {
				t.Errorf("trial %d: %v", tr.id, err)
				return
			}
		}
	}
}

func (w *randomMerges) trial(t *testing.T, tr realTrial, st *realStats) error {
	id := fmt.Sprint(tr.id)
	if _, err := w.r.Run(nil, "checkout", "-q", "-f", "--detach", "o"+id); err != nil {
		return err
	}
	defer w.r.Run(nil, "reset", "-q", "--hard")
	args := append([]string{"-c", "merge.conflictStyle=" + tr.style, "merge", "-q", "--no-edit"}, tr.x...)
	if _, err := w.r.Run(nil, append(args, "t"+id)...); err == nil {
		return nil
	}
	if u, _ := w.r.Run(nil, "ls-files", "-u", "--", realFile); u == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(w.r.Dir, realFile))
	if err != nil {
		return err
	}
	marked := false
	for _, n := range w.sizes {
		marked = marked || mergedSides{Base: []byte(tr.base), Ours: []byte(tr.ours), Theirs: []byte(tr.theirs)}.holdsMarkers(n)
	}
	segs, err := Load(context.Background(), testrun.Runner(t), w.r.GitDir(), w.r.Dir, realFile, data)
	st.mu.Lock()
	st.conflicted++
	if marked {
		st.marked++
	}
	st.mu.Unlock()
	if errors.Is(err, ErrAmbiguous) {
		st.mu.Lock()
		st.refused++
		if marked {
			st.markedRefused++
		}
		st.mu.Unlock()
		return nil
	}
	if err != nil {
		return err
	}
	var bad []string
	for _, side := range []struct {
		x  string
		ch Choice
	}{{"ours", Ours}, {"theirs", Theirs}} {
		out, _ := w.r.Run(nil, append(append([]string{"merge-tree", "--write-tree", "-X", side.x}, tr.x...), "o"+id, "t"+id)...)
		tree, _, _ := strings.Cut(out, "\n")
		blob, err := w.r.Run(nil, "rev-parse", tree+":"+realFile)
		if err != nil {
			return err
		}
		want, err := w.r.Run(nil, "cat-file", "--filters", "--path="+realFile, strings.TrimSpace(blob))
		if err != nil {
			return err
		}
		cs := make([]Choice, Count(segs))
		for i := range cs {
			cs[i] = side.ch
		}
		got, err := Resolve(segs, cs, nil)
		switch {
		case err == nil && string(got) == want:
		case err == nil && ignoresSpace(tr.x) && sameBesideSpace(string(got), want):
			// Ignoring white space, git takes a line both sides have
			// alike from ours, and so does -X theirs, but not always
			// the same way: the lines must be the same, their white
			// space and the file's last newline may differ.
			st.mu.Lock()
			st.space++
			st.mu.Unlock()
		default:
			bad = append(bad, fmt.Sprintf("taking %s: %q, %v, want %q", side.x, got, err, want))
		}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.read++
	if bad != nil {
		st.wrong = append(st.wrong, fmt.Sprintf("%s %v, attributes %q then %q, %d conflicts\nbase %q\nours %q\ntheirs %q\nmerged %q\n%s",
			tr.style, tr.x, w.attrs, w.theirs, Count(segs), tr.base, tr.ours, tr.theirs, data, strings.Join(bad, "\n")))
	}
	return nil
}
