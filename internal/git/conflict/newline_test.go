package conflict

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ramus/internal/git/gittest/testrun"
)

// A conflict that ends the file, a side of which has no newline at its
// end, is taken as git checkout --ours or --theirs writes that side: git
// adds the newline before the marker that follows the side, and it is not
// the side's. Each side with and without one, in LF, CRLF blobs and a
// checkout in CRLF, in every style, and sides that differ only in it.
func TestTakingASideKeepsItsEnd(t *testing.T) {
	lines := "a\nb\nc\nd\ne\nf\ng\nh\n"
	nl := func(s string, with bool) string {
		if with {
			return s + "\n"
		}
		return s
	}
	type sides struct{ base, ours, theirs string }
	var cases []struct {
		name string
		s    sides
	}
	for _, b := range []bool{true, false} {
		for _, o := range []bool{true, false} {
			for _, th := range []bool{true, false} {
				cases = append(cases, struct {
					name string
					s    sides
				}{
					name: strings.Join([]string{map[bool]string{true: "base nl", false: "base no nl"}[b],
						map[bool]string{true: "ours nl", false: "ours no nl"}[o], map[bool]string{true: "theirs nl", false: "theirs no nl"}[th]}, ", "),
					s: sides{nl(lines+"end", b), nl(lines+"end ours", o), nl(lines+"end theirs", th)},
				})
			}
		}
	}
	cases = append(cases, struct {
		name string
		s    sides
	}{"only the newline differs", sides{lines + "old\n", lines + "end", lines + "end\n"}},
		struct {
			name string
			s    sides
		}{"only the newline differs, theirs without", sides{lines + "old\n", lines + "end\n", lines + "end"}})
	crlf := func(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }
	for _, eol := range []struct {
		name, attrs string
		conv        func(string) string
	}{
		{"lf", "", func(s string) string { return s }},
		{"checkout crlf", "* text eol=crlf\n", func(s string) string { return s }},
		{"crlf blobs", "* -text\n", crlf},
	} {
		for _, c := range cases {
			for _, style := range []string{"merge", "diff3", "zdiff3"} {
				t.Run(eol.name+"/"+c.name+"/"+style, func(t *testing.T) {
					t.Parallel()
					m := realMerge{base: eol.conv(c.s.base), ours: eol.conv(c.s.ours), theirs: eol.conv(c.s.theirs)}
					if eol.attrs != "" {
						m.attrs = ptr(eol.attrs)
					}
					r, ours, theirs, _ := m.run(t, "merge", style)
					if r == nil {
						t.Fatal("no conflict in the file")
					}
					segs, data, err := loadMerged(t, r)
					if err != nil || Count(segs) != 1 {
						t.Fatalf("%d conflicts, %v\n%q", Count(segs), err, data)
					}
					checkSides(t, r, segs, data, ours, theirs, "", nil)
					for _, side := range []struct {
						flag string
						ch   Choice
					}{{"--ours", Ours}, {"--theirs", Theirs}} {
						r.Git("checkout", side.flag, "--", realFile)
						want, err := os.ReadFile(filepath.Join(r.Dir, realFile))
						if err != nil {
							t.Fatal(err)
						}
						if got, err := Resolve(segs, []Choice{side.ch}, nil); err != nil || string(got) != string(want) {
							t.Errorf("%s: %q, %v\nwant %q\nmerged %q", side.flag, got, err, want, data)
						}
					}
				})
			}
		}
	}
}

// The merge tool's sides are taken as they end too: as git merge-file
// --ours or --theirs writes them.
func TestMergeFilesKeepsTheSidesEnds(t *testing.T) {
	dir := t.TempDir()
	put := func(name, s string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	lines := "a\nb\nc\nd\ne\nf\ng\nh\n"
	for _, c := range []struct{ base, local, remote string }{
		{lines + "end", lines + "end local", lines + "end remote"},
		{lines + "end\n", lines + "end local", lines + "end remote\n"},
		{lines + "end\r\n", lines + "end local\r", lines + "end remote"},
		{"x\r\n" + lines + "end", "x\r\n" + lines + "end local", "x\r\n" + lines + "end remote"},
	} {
		base, local, remote := put("base", c.base), put("local", c.local), put("remote", c.remote)
		f, err := MergeFiles(context.Background(), testrun.Runner(t), base, local, remote, [3]string{"L", "B", "R"})
		if err != nil || Count(f.Segments) != 1 {
			t.Fatalf("%q: %+v, %v", c, f, err)
		}
		for _, side := range []struct {
			flag   string
			choice Choice
		}{{"--ours", Ours}, {"--theirs", Theirs}} {
			want, _ := mergeFile(t, side.flag, local, base, remote)
			if got, err := Resolve(f.Segments, []Choice{side.choice}, nil); err != nil || string(got) != want {
				t.Errorf("%q, %s: %q, %v; want %q", c, side.flag, got, err, want)
			}
		}
	}
}
