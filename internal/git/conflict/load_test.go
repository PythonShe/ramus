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
			ours: lines + example, their: lines + "end theirs\n", ambiguous: true},
		{name: "close marker in their side", base: lines + "end\n",
			ours: lines + "end ours\n", their: lines + "end theirs\n>>>>>>> quoted\nmore\n", ambiguous: true},
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
	cmd.Env = gittest.Env()
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
