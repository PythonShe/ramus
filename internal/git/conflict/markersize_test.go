package conflict

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

// realMerge is a file merged by a real git operation from a base commit:
// main changes it to ours, topic to theirs, each with its .gitattributes
// (nil leaves the base's).
type realMerge struct {
	attrs, oursAttrs, theirsAttrs *string
	base, ours, theirs            string
	// args go before the operation, as -c settings.
	args []string
	// x are the operation's -X options.
	x []string
}

const realFile = "f.md"

// run makes the merge with op ("merge", "cherry-pick", "rebase" or
// "stash", a pop) in style and returns the repository, or nil when the
// operation did not stop on a conflict in the file. ours and theirs are
// the commits to merge for the oracle, and mergeBase the base when it is
// not theirs' merge base with ours.
func (c realMerge) run(t *testing.T, op, style string) (r *gittest.Repo, ours, theirs, mergeBase string) {
	t.Helper()
	r = gittest.Init(t)
	attrs := func(a *string) {
		if a != nil {
			r.Write(".gitattributes", *a)
		}
	}
	attrs(c.attrs)
	r.Write(realFile, c.base)
	r.Write("other", "x\n")
	base := r.Commit("base")
	r.Git("checkout", "-q", "-b", "topic")
	r.Write(realFile, c.theirs)
	attrs(c.theirsAttrs)
	r.Commit("theirs")
	r.Git("checkout", "-q", "main")
	r.Write(realFile, c.ours)
	attrs(c.oursAttrs)
	r.Commit("ours")
	args := append(append([]string(nil), c.args...), "-c", "merge.conflictStyle="+style)
	var err error
	ours, theirs = "main", "topic"
	switch op {
	case "merge":
		_, err = r.Run(nil, append(append(args, "merge", "-q"), append(c.x, "topic")...)...)
	case "cherry-pick":
		_, err = r.Run(nil, append(append(args, "cherry-pick"), append(c.x, "topic")...)...)
		mergeBase = "topic^"
	case "rebase":
		r.Git("checkout", "-q", "topic")
		_, err = r.Run(nil, append(append(args, "rebase"), append(c.x, "main")...)...)
		mergeBase = "topic^"
	case "stash":
		r.Git("checkout", "-q", "--detach", base)
		r.Write(realFile, c.theirs)
		attrs(c.theirsAttrs)
		r.Git("stash", "push", "-q")
		theirs = strings.TrimSpace(r.Git("rev-parse", "refs/stash"))
		r.Git("checkout", "-q", "main")
		_, err = r.Run(nil, append(args, "stash", "pop")...)
		mergeBase = base
	}
	if err == nil || !strings.Contains(r.Git("ls-files", "-u", "--", realFile), realFile) {
		return nil, "", "", ""
	}
	return r, ours, theirs, mergeBase
}

// loadMerged reads the merged file as the merge editor does.
func loadMerged(t *testing.T, r *gittest.Repo) ([]Segment, []byte, error) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(r.Dir, realFile))
	if err != nil {
		t.Fatal(err)
	}
	segs, err := Load(context.Background(), testrun.Runner(t), r.GitDir(), r.Dir, realFile, data)
	return segs, data, err
}

// checkSides fails unless taking every conflict of segs by one side gives
// what git merges taking that side in each conflict (merge-tree -X ours
// or theirs, as checkout writes it), for both sides.
func checkSides(t *testing.T, r *gittest.Repo, segs []Segment, data []byte, ours, theirs, mergeBase string, x []string) {
	t.Helper()
	for _, side := range []struct {
		x  string
		ch Choice
	}{{"ours", Ours}, {"theirs", Theirs}} {
		args := append([]string{"merge-tree", "--write-tree", "-X", side.x}, x...)
		if mergeBase != "" {
			args = append(args, "--merge-base="+mergeBase)
		}
		out, _ := r.Run(nil, append(args, ours, theirs)...)
		tree, _, _ := strings.Cut(out, "\n")
		blob := strings.TrimSpace(r.Git("rev-parse", tree+":"+realFile))
		want := r.Git("cat-file", "--filters", "--path="+realFile, blob)
		cs := make([]Choice, Count(segs))
		for i := range cs {
			cs[i] = side.ch
		}
		if got, err := Resolve(segs, cs, nil); err != nil || string(got) != want {
			t.Errorf("taking %s: %q, %v\nwant %q\nmerged %q", side.x, got, err, want, data)
		}
	}
}

func ptr(s string) *string { return &s }

// git writes the markers in the size the attributes gave when it merged,
// before the merge changed them: a merged branch that sets, changes or
// removes the file's conflict-marker-size leaves markers of the size
// before it. Every operation, in every style, with and without text like
// markers in the sides, is read as git merged it.
func TestLoadWhenTheMergeChangesTheMarkerSize(t *testing.T) {
	lines := "a\nb\nc\nd\ne\nf\ng\nh\n"
	plain := realMerge{base: lines + "end\n", ours: lines + "end ours\n", theirs: lines + "end theirs\n"}
	examples := realMerge{base: example + lines + "end\n", ours: example + lines + "end ours\n", theirs: example + lines + "end theirs\n"}
	size10, size12 := ptr(realFile+" conflict-marker-size=10\n"), ptr(realFile+" conflict-marker-size=12\n")
	var cases []struct {
		name string
		c    realMerge
	}
	for _, m := range []struct {
		name string
		c    realMerge
	}{{"plain", plain}, {"examples", examples}} {
		add := func(name string, f func(*realMerge)) {
			c := m.c
			f(&c)
			cases = append(cases, struct {
				name string
				c    realMerge
			}{m.name + ", " + name, c})
		}
		add("theirs sets it", func(c *realMerge) { c.theirsAttrs = size10 })
		add("theirs removes it", func(c *realMerge) { c.attrs, c.theirsAttrs = size10, ptr("") })
		add("ours sets it", func(c *realMerge) { c.oursAttrs = size10 })
		add("both change it", func(c *realMerge) { c.attrs, c.oursAttrs, c.theirsAttrs = ptr(""), size10, size12 })
	}
	for _, c := range cases {
		for _, op := range []string{"merge", "cherry-pick", "rebase", "stash"} {
			for _, style := range []string{"merge", "diff3", "zdiff3"} {
				t.Run(c.name+"/"+op+"/"+style, func(t *testing.T) {
					t.Parallel()
					r, ours, theirs, mb := c.c.run(t, op, style)
					if r == nil {
						t.Fatal("no conflict in the file")
					}
					segs, data, err := loadMerged(t, r)
					if err != nil || Count(segs) != 1 {
						t.Fatalf("%d conflicts, %v\n%s", Count(segs), err, data)
					}
					checkSides(t, r, segs, data, ours, theirs, mb, nil)
				})
			}
		}
	}
}

// A file whose markers are of a size neither HEAD's nor the work tree's
// attributes give, as from a merge with attributes of its own, is read at
// the size of its own marker lines, checked by merging the sides again,
// never as having no conflicts.
func TestLoadFindsTheMarkerSizeInTheFile(t *testing.T) {
	lines := "a\nb\nc\nd\ne\nf\ng\nh\n"
	for _, c := range []struct {
		name string
		c    realMerge
	}{
		{"plain", realMerge{base: lines + "end\n", ours: lines + "end ours\n", theirs: lines + "end theirs\n"}},
		{"examples", realMerge{base: example + lines + "end\n", ours: example + lines + "end ours\n", theirs: example + lines + "end theirs\n"}},
		{"example in a conflict", realMerge{base: lines + "end\n", ours: lines + example, theirs: lines + "end theirs\n"}},
	} {
		for _, style := range []string{"merge", "diff3", "zdiff3"} {
			t.Run(c.name+"/"+style, func(t *testing.T) {
				attrs := filepath.Join(t.TempDir(), "attributes")
				if err := os.WriteFile(attrs, []byte(realFile+" conflict-marker-size=10\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				c.c.args = []string{"-c", "core.attributesFile=" + attrs}
				r, ours, theirs, mb := c.c.run(t, "merge", style)
				if r == nil {
					t.Fatal("no conflict in the file")
				}
				segs, data, err := loadMerged(t, r)
				if !strings.Contains(string(data), "\n==========\n") {
					t.Fatalf("not merged with markers of 10\n%s", data)
				}
				if err != nil || Count(segs) != 1 {
					t.Fatalf("%d conflicts, %v\n%s", Count(segs), err, data)
				}
				checkSides(t, r, segs, data, ours, theirs, mb, nil)
			})
		}
	}
}

// A file the index holds conflicted is never read as having no conflicts,
// or fewer than it has, while git's markers are in it; a file resolved by
// hand, its markers gone, is, whatever text like markers its sides gave
// it.
func TestLoadNeverMissesConflicts(t *testing.T) {
	// Ignoring white space at the end of lines, git matches our last
	// line, which has no newline, with the base's, and writes the open
	// marker of the conflict after it on the same line: alone, and after
	// a conflict read.
	for _, c := range []struct {
		name   string
		c      realMerge
		styles []string
	}{
		{"alone", realMerge{base: "c\nd\nd\nb\n  }\nb\nb\n", ours: "c\nd\nd\nb\n  }\nb", theirs: "c\na\nd\n  }\nb\nb\nc\n"},
			[]string{"merge", "diff3", "zdiff3"}},
		{"after another", realMerge{base: "d\n==========\nb\nb\nc\nf", ours: "d\n==========\nb\n}\nb\nc",
			theirs: "d\n==========\nf\nb\nc\n<<<<<<<<<< HEAD\na\n", theirsAttrs: ptr(realFile + " conflict-marker-size=10\n")},
			[]string{"diff3", "zdiff3"}},
	} {
		c.c.x = []string{"-X", "ignore-space-at-eol"}
		for _, style := range c.styles {
			t.Run("open marker on a line, "+c.name+"/"+style, func(t *testing.T) {
				r, _, _, _ := c.c.run(t, "merge", style)
				if r == nil {
					t.Fatal("no conflict in the file")
				}
				segs, data, err := loadMerged(t, r)
				if !regexp.MustCompile(`[^\n<]<<<<<<< HEAD\n`).Match(data) {
					t.Fatalf("no open marker on a line\n%q", data)
				}
				if !errors.Is(err, ErrAmbiguous) {
					t.Fatalf("%d conflicts, %v\n%q", Count(segs), err, data)
				}
			})
		}
	}
	lines := "a\nb\nc\nd\ne\nf\ng\nh\n"
	for _, c := range []struct {
		name  string
		c     realMerge
		edit  func(string) string
		read  bool // as having no conflicts; else refused
		style string
	}{
		{name: "open marker taken out by hand", c: realMerge{base: lines + "end\n", ours: lines + "end ours\n", theirs: lines + "end theirs\n"},
			edit: func(s string) string { return strings.Replace(s, "<<<<<<< HEAD\n", "", 1) }},
		{name: "resolved by hand", c: realMerge{base: lines + "end\n", ours: lines + "end ours\n", theirs: lines + "end theirs\n"},
			edit: func(string) string { return lines + "end both\n" }, read: true},
		{name: "resolved by hand, examples kept", c: realMerge{attrs: ptr(realFile + " conflict-marker-size=10\n"),
			base: example + lines + "end\n", ours: example + lines + "end ours\n", theirs: example + lines + "end theirs\n"},
			edit: func(string) string { return example + lines + "end both\n" }, read: true},
		{name: "resolved by hand, setext underlines kept", c: realMerge{
			base: "Title\n=======\n> quote\n" + lines + "end\n", ours: "Title\n=======\n> quote\n" + lines + "end ours\n", theirs: "Title\n=======\n> quote\n" + lines + "end theirs\n"},
			edit: func(string) string { return "Title\n=======\n> quote\n" + lines + "end both\n" }, read: true},
	} {
		for _, style := range []string{"merge", "diff3"} {
			t.Run(c.name+"/"+style, func(t *testing.T) {
				r, _, _, _ := c.c.run(t, "merge", style)
				if r == nil {
					t.Fatal("no conflict in the file")
				}
				p := filepath.Join(r.Dir, realFile)
				data, err := os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(c.edit(string(data))), 0o644); err != nil {
					t.Fatal(err)
				}
				segs, data, err := loadMerged(t, r)
				if c.read && (err != nil || Count(segs) != 0 || string(data) != c.edit("")) {
					t.Fatalf("%d conflicts, %v\n%s", Count(segs), err, data)
				}
				if !c.read && !errors.Is(err, ErrAmbiguous) {
					t.Fatalf("%d conflicts, %v\n%s", Count(segs), err, data)
				}
			})
		}
	}
}
