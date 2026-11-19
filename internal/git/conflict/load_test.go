package conflict

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

const example = "<<<<<<< HEAD\nyour change\n=======\ntheir change\n>>>>>>> topic\n"

// TestLoadAgainstGit merges files that hold text like conflict markers
// with git, in every conflict style, and reads what git left. git is the
// oracle: the conflicts are as many as git merge-file counts, and taking
// every one by a side gives what git merge-file writes for that side, so
// no line outside a conflict is lost. What cannot be told apart is
// refused.
func TestLoadAgainstGit(t *testing.T) {
	lines := "a\nb\nc\nd\ne\nf\ng\nh\n"
	for _, c := range []struct {
		name  string
		attrs string
		base  string
		ours  string
		their string
		// ambiguous is a merge that cannot be read without a
		// conflict-marker-size attribute.
		ambiguous bool
		// edit changes the conflict by hand before it is read.
		edit bool
	}{
		{name: "example both sides hold", base: "Intro\n\n" + example + "\n" + lines + "end\n",
			ours: "Intro\n\n" + example + "\n" + lines + "end ours\n", their: "Intro\n\n" + example + "\n" + lines + "end theirs\n"},
		{name: "example one side added", base: lines + "end\n",
			ours: lines + "end ours\n", their: "a\n" + example + "b\nc\nd\ne\nf\ng\nh\nend theirs\n"},
		{name: "example after the conflict", base: "start\n" + lines + example,
			ours: "start ours\n" + lines + example, their: "start theirs\n" + lines + example},
		{name: "two conflicts around an example", base: "start\n" + lines + example + lines + "end\n",
			ours: "start ours\n" + lines + example + lines + "end ours\n", their: "start theirs\n" + lines + example + lines + "end theirs\n"},
		{name: "open marker alone before", base: "<<<<<<< x\n" + lines + "end\n",
			ours: "<<<<<<< x\n" + lines + "end ours\n", their: "<<<<<<< x\n" + lines + "end theirs\n"},
		{name: "open marker alone after", base: "start\n" + lines + "<<<<<<< x\n",
			ours: "start ours\n" + lines + "<<<<<<< x\n", their: "start theirs\n" + lines + "<<<<<<< x\n"},
		{name: "underline", base: "Title\n=======\n" + lines + "end\n",
			ours: "Title\n=======\n" + lines + "end ours\n", their: "Title\n=======\n" + lines + "end theirs\n"},
		{name: "CRLF", attrs: "README.md eol=crlf\n", base: "Intro\n" + example + lines + "end\n",
			ours: "Intro\n" + example + lines + "end ours\n", their: "Intro\n" + example + lines + "end theirs\n"},
		{name: "example in our side", base: lines + "end\n",
			ours: lines + example, their: lines + "end theirs\n"},
		{name: "close marker in their side", base: lines + "end\n",
			ours: lines + "end ours\n", their: lines + "end theirs\n>>>>>>> quoted\nmore\n"},
		{name: "close marker with the merged branch's label in their side", base: lines + "end\n",
			ours: lines + "end ours\n", their: lines + "end theirs\n>>>>>>> topic\nmore\n"},
		{name: "example's open line edited on both sides", base: "Intro\n" + example + "end\n",
			ours:  "Intro\n<<<<<<< HEAD\nyour change, edited by us\n=======\ntheir change\n>>>>>>> topic\nend\n",
			their: "Intro\n<<<<<<< HEAD\nyour change, edited by them\n=======\ntheir change\n>>>>>>> topic\nend\n"},
		{name: "example's body edited on both sides", base: "Intro\n" + example + "end\n",
			ours:  "Intro\n<<<<<<< HEAD\nyour change\n=======\ntheir change, ours\n>>>>>>> topic\nend\n",
			their: "Intro\n<<<<<<< HEAD\nyour change\n=======\ntheir change, theirs\n>>>>>>> topic\nend\n"},
		{name: "underline ours adds", base: "Title\n=======\n\nSection\n=======\nold\n",
			ours: "Title\n=======\n\nSection\n=======\nours\n=======\n", their: "Title\n=======\n\nSection\n=======\ntheirs\n"},
		{name: "base marker last in ours", base: "a\nold\nz\n", ours: "a\nours\n||||||| note\nz\n", their: "a\ntheirs\nz\n"},
		{name: "base marker first in ours", base: "a\nold\nz\n", ours: "a\n||||||| note\nours\nz\n", their: "a\ntheirs\nz\n"},
		{name: "conflict like an example elsewhere", base: example + lines + "old\n",
			ours: example + lines + "your change\n", their: example + lines + "their change\n"},
		{name: "example in our side, edited by hand", base: lines + "end\n",
			ours: lines + example, their: lines + "end theirs\n", edit: true, ambiguous: true},
		{name: "example in our side, marker size", attrs: "README.md conflict-marker-size=10\n", base: lines + "end\n",
			ours: lines + example, their: lines + "end theirs\n"},
		{name: "example both sides hold, marker size", attrs: "README.md conflict-marker-size=10\n", base: example + lines + "end\n",
			ours: example + lines + "end ours\n", their: example + lines + "end theirs\n"},
	} {
		for _, style := range []string{"merge", "diff3", "zdiff3"} {
			t.Run(c.name+"/"+style, func(t *testing.T) {
				r := gittest.Init(t)
				if c.attrs != "" {
					r.Write(".gitattributes", c.attrs)
				}
				r.Write("README.md", c.base)
				r.Commit("base")
				r.Git("checkout", "-q", "-b", "topic")
				r.Write("README.md", c.their)
				r.Commit("theirs")
				r.Git("checkout", "-q", "main")
				r.Write("README.md", c.ours)
				r.Commit("ours")
				// git merge-file, the oracle, diffs as myers does.
				if _, err := r.Run(nil, "-c", "merge.conflictStyle="+style, "merge", "-q", "-X", "diff-algorithm=myers", "topic"); err == nil {
					t.Fatal("the merge did not conflict")
				}
				data, err := os.ReadFile(filepath.Join(r.Dir, "README.md"))
				if err != nil {
					t.Fatal(err)
				}
				if c.edit {
					data = []byte(strings.Replace(string(data), "end theirs\n", "end, both\n", 1))
				}
				segs, err := Load(context.Background(), testrun.Runner(t), r.GitDir(), r.Dir, "README.md", data)
				if c.ambiguous {
					if !errors.Is(err, ErrAmbiguous) {
						t.Fatalf("read as %d conflicts, %v\n%s", Count(segs), err, data)
					}
					return
				}
				if err != nil {
					t.Fatalf("%v\n%s", err, data)
				}
				dir := t.TempDir()
				var stages []string
				for _, n := range []string{"2", "1", "3"} {
					p := filepath.Join(dir, n)
					os.WriteFile(p, []byte(r.Git("show", ":"+n+":README.md")), 0o644)
					stages = append(stages, p)
				}
				args := stages
				if style != "merge" {
					args = append([]string{"--" + style}, stages...)
				}
				_, want := mergeFile(t, args...)
				if n := Count(segs); n != want {
					t.Fatalf("%d conflicts, git counts %d\n%s", n, want, data)
				}
				for _, side := range []struct {
					flag   string
					choice Choice
				}{{"--ours", Ours}, {"--theirs", Theirs}, {"--union", OursThenTheirs}} {
					cs := make([]Choice, Count(segs))
					for i := range cs {
						cs[i] = side.choice
					}
					got, err := Resolve(segs, cs, nil)
					want, _ := mergeFile(t, append([]string{side.flag}, stages...)...)
					if strings.Contains(c.attrs, "crlf") {
						want = strings.ReplaceAll(want, "\n", "\r\n")
					}
					if err != nil || string(got) != want {
						t.Errorf("%s: %v\n%s\nwant\n%s", side.flag, err, got, want)
					}
				}
			})
		}
	}
}

// mergeFile runs git merge-file -p and returns what it writes and the
// number of conflicts, its exit status.
func mergeFile(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"merge-file", "-p"}, args...)...)
	// Outside any repository: the one around the test would lend git its
	// config, or stop it when it does not resolve.
	cmd.Dir, cmd.Env = t.TempDir(), gittest.Env()
	out, err := cmd.Output()
	var e *exec.ExitError
	if errors.As(err, &e) && e.ExitCode() > 0 && e.ExitCode() < 128 {
		return string(out), e.ExitCode()
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(out), 0
}

// The conflict-marker-size attribute is read as git reads it when it
// merges: a number that fits in a C int and is positive, else the
// default. At the bound, git writes markers of 2 GB each (a file of 6 GB
// for one conflict), so the bound is checked on the value alone.
func TestMarkerSizeAsGitReadsIt(t *testing.T) {
	for v, want := range map[string]int{
		"2147483647": 2147483647, "2147483648": 7, "4294967303": 7, "-2147483648": 7,
		"0": 7, "-3": 7, "-0": 7, "+9": 9, "010": 10, "10x": 7, "x": 7, "": 7,
		"set": 7, "unset": 7, "unspecified": 7, "0x10": 7, "1e3": 7,
	} {
		if got := markerSizeOf(v); got != want {
			t.Errorf("%q: %d, want %d", v, got, want)
		}
	}
	lines := "a\nb\nc\nd\ne\nf\ng\nh\n"
	for _, v := range []string{"2147483648", "4294967303", "0", "-3", "x", "10x", "+9", "010"} {
		t.Run(v, func(t *testing.T) {
			r := gittest.Init(t)
			r.Write(".gitattributes", "f.md conflict-marker-size="+v+"\n")
			r.Write("f.md", lines+"end\n")
			r.Commit("base")
			r.Git("checkout", "-q", "-b", "topic")
			r.Write("f.md", lines+"end theirs\n")
			r.Commit("theirs")
			r.Git("checkout", "-q", "main")
			r.Write("f.md", lines+"end ours\n")
			r.Commit("ours")
			if _, err := r.Run(nil, "merge", "-q", "topic"); err == nil {
				t.Fatal("the merge did not conflict")
			}
			data, err := os.ReadFile(filepath.Join(r.Dir, "f.md"))
			if err != nil {
				t.Fatal(err)
			}
			segs, err := Load(context.Background(), testrun.Runner(t), r.GitDir(), r.Dir, "f.md", data)
			if err != nil || Count(segs) != 1 {
				t.Fatalf("%d conflicts, %v\n%s", Count(segs), err, data)
			}
			for _, side := range []struct {
				choice Choice
				want   string
			}{{Ours, lines + "end ours\n"}, {Theirs, lines + "end theirs\n"}} {
				if got, err := Resolve(segs, []Choice{side.choice}, nil); err != nil || string(got) != side.want {
					t.Errorf("%v: %q, %v", side.choice, got, err)
				}
			}
		})
	}
}

// Two renames onto one path, each with a conflict of its own, leave a
// file whose sides in the index hold git's nested conflicts, in markers
// one longer: any choice would keep them as text, so the file is
// refused, in every style, with an example in it or not.
func TestLoadRefusesNestedConflicts(t *testing.T) {
	for _, pre := range []string{"", example} {
		for _, style := range []string{"merge", "diff3", "zdiff3"} {
			r := gittest.Init(t)
			r.Write("a.txt", pre+"a1\na2\na3\na4\na5\na6\n")
			r.Write("b.txt", pre+"b1\nb2\nb3\nb4\nb5\nb6\n")
			r.Commit("base")
			r.Git("checkout", "-q", "-b", "topic")
			r.Write("a.txt", pre+"a1\na2 topic\na3\na4\na5\na6\n")
			r.Git("mv", "b.txt", "f.md")
			r.Write("f.md", pre+"b1\nb2\nb3\nb4\nb5 topic\nb6\n")
			r.Commit("theirs")
			r.Git("checkout", "-q", "main")
			r.Git("mv", "a.txt", "f.md")
			r.Write("f.md", pre+"a1\na2 main\na3\na4\na5\na6\n")
			r.Write("b.txt", pre+"b1\nb2\nb3\nb4\nb5 main\nb6\n")
			r.Commit("ours")
			r.Run(nil, "-c", "merge.conflictStyle="+style, "merge", "topic")
			data, err := os.ReadFile(filepath.Join(r.Dir, "f.md"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), "\n<<<<<<<< ") {
				t.Fatalf("%s: git nested no conflict: %q", style, data)
			}
			segs, err := Load(context.Background(), testrun.Runner(t), r.GitDir(), r.Dir, "f.md", data)
			if !errors.Is(err, ErrAmbiguous) {
				t.Fatalf("%s, example %v: %d conflicts, %v", style, pre != "", Count(segs), err)
			}
		}
	}
}
