package diff

import (
	"bytes"
	"context"
	"errors"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

func TestParse(t *testing.T) {
	text := "diff --git a/f b/f\nindex 1..2 100644\n--- a/f\n+++ b/f\n@@ -1,3 +1,3 @@ func\n a\n-b\n+B\n c\n@@ -9 +9,0 @@\n-z\n\\ No newline at end of file\n" +
		"diff --git a/bin b/bin\nindex 1..2 100644\nBinary files a/bin and b/bin differ\n"
	p, err := Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Files) != 2 || len(p.Hunks) != 2 || len(p.Lines) != 6 {
		t.Fatalf("files %d hunks %d lines %d", len(p.Files), len(p.Hunks), len(p.Lines))
	}
	if p.Path(0) != "f" || p.Path(1) != "bin" || !p.Files[1].Binary || p.Files[0].Binary {
		t.Fatalf("files %+v", p.Files)
	}
	if h := p.Hunks[1]; h.OldStart != 9 || h.OldCount != 1 || h.NewCount != 0 {
		t.Fatalf("hunk %+v", h)
	}
	if l := p.Lines[2]; l.Kind != '+' || string(p.Bytes(l.Text)) != "B" || l.New != 2 || l.Old != 0 {
		t.Fatalf("line %+v", l)
	}
	if !strings.HasSuffix(string(p.Bytes(p.Files[0].Header)), "+++ b/f\n") {
		t.Fatalf("header %q", p.Bytes(p.Files[0].Header))
	}
	if _, err := Parse([]byte("@@ -1 +1 @@\n")); !errors.Is(err, ErrBadPatch) {
		t.Fatalf("hunk with no file: %v", err)
	}
}

// A hunk whose header counts fewer lines than follow it is refused, not
// cut short at the count: the lines past it would be lost, and a "--- " or
// "+++ " among them taken for the file's paths.
func TestParseRefusesUndercountedHunk(t *testing.T) {
	head := "diff --git a/f b/f\nindex 1..2 100644\n--- a/f\n+++ b/f\n"
	for _, body := range []string{
		"@@ -1 +1 @@\n-a\n+b\n+c\n",
		"@@ -1 +1 @@\n-a\n+b\n c\n",
		"@@ -1,2 +1 @@\n-a\n-b\n--- x\n",
		"@@ -1 +1 @@\n-a\n+b\n+++ y\n",
	} {
		if _, err := Parse([]byte(head + body)); !errors.Is(err, ErrBadPatch) {
			t.Errorf("%q: %v", body, err)
		}
	}
	// What may follow a hunk that adds up: another hunk, the next file,
	// the end, or a blank line, as git log -L leaves between commits.
	ok := head + "@@ -1 +1 @@\n-a\n+b\n@@ -5 +5 @@\n-e\n+E\n\n" + "diff --git a/g b/g\n--- a/g\n+++ b/g\n@@ -1 +1 @@\n-x\n+y\n\\ No newline at end of file\n"
	if p, err := Parse([]byte(ok)); err != nil || len(p.Files) != 2 || len(p.Lines) != 7 {
		t.Fatalf("%v %+v", err, p)
	}
}

// TestSelectAgainstGit stages, discards and unstages random choices of
// lines of random edits through git apply, and checks the result against
// what the choice means, line by line: with lines ending in a carriage
// return, paths with spaces and outside ASCII, and core.autocrlf.
func TestSelectAgainstGit(t *testing.T) {
	for i, v := range []selectCase{
		{name: "plain", path: "f"},
		{name: "carriage returns", path: "a b.txt", cr: true},
		{name: "non-ASCII path", path: "dir ü/é f.txt"},
		{name: "unstage", path: "dir ü/é f.txt", cr: true, staged: true},
		{name: "autocrlf", path: "a b.txt", autocrlf: true},
	} {
		t.Run(v.name, func(t *testing.T) {
			t.Parallel()
			selectAgainstGit(t, v, uint64(i))
		})
	}
}

type selectCase struct {
	name, path string
	// cr puts lines ending in a carriage return in the files.
	cr bool
	// staged unstages lines from the index, where the others stage and
	// discard them.
	staged bool
	// autocrlf keeps the work tree in CRLF and the index in LF.
	autocrlf bool
}

func selectAgainstGit(t *testing.T, v selectCase, seed uint64) {
	r := gittest.Init(t)
	rn := testrun.Runner(t)
	rng := rand.New(rand.NewPCG(1, 2+seed))
	words := []string{"alpha", "beta", "", "gamma", "delta", "beta", "  indented", "}"}
	if v.cr {
		words = append(words, "cr\r", "\r", "mixed\r")
	}
	onDisk := func(s string) string { return s }
	if v.autocrlf {
		r.Git("config", "core.autocrlf", "true")
		onDisk = func(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }
	}
	text := func(n int, eol bool) string {
		var b strings.Builder
		for i := range n {
			b.WriteString(words[rng.IntN(len(words))])
			if i < n-1 || eol {
				b.WriteByte('\n')
			}
		}
		return b.String()
	}
	edit := func(old string) string {
		lines := strings.SplitAfter(old, "\n")
		var out []string
		for _, l := range lines {
			switch rng.IntN(8) {
			case 0: // removed
			case 1:
				out = append(out, words[rng.IntN(len(words))]+"\n", l)
			case 2:
				out = append(out, strings.TrimSuffix(l, "\n")+" changed\n")
			default:
				out = append(out, l)
			}
		}
		s := strings.Join(out, "")
		if rng.IntN(3) == 0 {
			s = strings.TrimSuffix(s, "\n")
		} else if rng.IntN(3) == 0 && !strings.HasSuffix(s, "\n") && s != "" {
			s += "\n"
		}
		return s
	}
	apply := func(patch []byte, args ...string) error {
		cmd := exec.Command(rn.Git().Path, append([]string{"apply", "--whitespace=nowarn"}, args...)...)
		cmd.Dir, cmd.Env, cmd.Stdin = r.Dir, gittest.Env(), bytes.NewReader(patch)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return errors.New(string(out))
		}
		return nil
	}
	// prepare puts old where the patch starts from and nu where it goes:
	// the index and the work tree, or HEAD and the index to unstage.
	prepare := func(old, nu string) {
		r.Write(v.path, onDisk(old))
		r.Git("add", "--", v.path)
		if v.staged {
			r.CommitIndex("old")
		}
		r.Write(v.path, onDisk(nu))
		if v.staged {
			r.Git("add", "--", v.path)
		}
	}
	src, directions := Unstaged, []bool{false, true}
	if v.staged {
		src, directions = Staged, []bool{true}
	}
	tried := 0
	for iter := range 40 {
		old := text(rng.IntN(30), rng.IntN(4) != 0)
		nu := edit(old)
		if nu == old {
			continue
		}
		prepare(old, nu)
		ctxLines := 1 + rng.IntN(3)
		pt, err := WorkTreePatch(context.Background(), rn, r.GitDir(), r.Dir, PatchRequest{Source: src, Context: ctxLines, ForApply: true})
		if err != nil {
			t.Fatal(err)
		}
		p, err := Parse(pt.Text)
		if err != nil || len(p.Files) != 1 || p.Path(0) != v.path {
			t.Fatalf("iter %d: %v\n%s", iter, err, pt.Text)
		}
		pick := map[int]bool{}
		for i, l := range p.Lines {
			if (l.Kind == '+' || l.Kind == '-') && rng.IntN(2) == 0 {
				pick[i] = true
			}
		}
		chosen := func(i int) bool { return pick[i] }
		for _, reverse := range directions {
			sel, err := p.Select(0, reverse, chosen)
			if errors.Is(err, ErrNothingChosen) {
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			want := expect(p, old, nu, reverse, chosen)
			var got string
			switch {
			case v.staged:
				// Unstage the chosen lines.
				if err := apply(sel, "-R", "--cached"); err != nil {
					t.Fatalf("iter %d unstage: %s\nold %q\nnew %q\npatch:\n%s\nselection:\n%s", iter, err, old, nu, pt.Text, sel)
				}
				got = r.Git("show", ":"+v.path)
				r.Git("add", "--", v.path)
			case reverse:
				// Discard the chosen lines from the work tree.
				if err := apply(sel, "-R"); err != nil {
					t.Fatalf("iter %d discard: %s\nold %q\nnew %q\npatch:\n%s\nselection:\n%s", iter, err, old, nu, pt.Text, sel)
				}
				b, _ := os.ReadFile(filepath.Join(r.Dir, filepath.FromSlash(v.path)))
				got, want = string(b), onDisk(want)
				r.Write(v.path, onDisk(nu))
			default:
				// Stage the chosen lines.
				if err := apply(sel, "--cached"); err != nil {
					t.Fatalf("iter %d stage: %s\nold %q\nnew %q\npatch:\n%s\nselection:\n%s", iter, err, old, nu, pt.Text, sel)
				}
				got = r.Git("show", ":"+v.path)
				prepare(old, nu)
			}
			if got != want {
				t.Fatalf("iter %d reverse=%v:\ngot  %q\nwant %q\nold %q\nnew %q\nselection:\n%s", iter, reverse, got, want, old, nu, sel)
			}
			tried++
		}
	}
	if tried < 25*len(directions) {
		t.Fatalf("only %d selections tried", tried)
	}
	t.Logf("%d selections", tried)
}

// expect is the file a selection makes: forward, the old side with the
// chosen changes made; reverse, the new side with the chosen changes
// taken back.
func expect(p *Parsed, old, nu string, reverse bool, chosen func(int) bool) string {
	base := splitLines(old)
	if reverse {
		base = splitLines(nu)
	}
	var out strings.Builder
	cursor := 0
	for hi := p.Files[0].Hunks.Start; hi < p.Files[0].Hunks.End; hi++ {
		h := p.Hunks[hi]
		start, count := h.OldStart, h.OldCount
		if reverse {
			start, count = h.NewStart, h.NewCount
		}
		before := int(start) - 1
		if count == 0 {
			before = int(start)
		}
		for ; cursor < before; cursor++ {
			out.WriteString(base[cursor])
		}
		for li := h.Lines.Start; li < h.Lines.End; li++ {
			l := p.Lines[li]
			eol := "\n"
			if li+1 < h.Lines.End && p.Lines[li+1].Kind == '\\' {
				eol = ""
			}
			keep := false
			switch l.Kind {
			case ' ':
				keep = true
			case '-':
				keep = chosen(int(li)) == reverse
			case '+':
				keep = chosen(int(li)) != reverse
			}
			if keep {
				// Only the last line of a file goes without a newline:
				// one that ended a side and has a line after it now has
				// one.
				if s := out.String(); s != "" && !strings.HasSuffix(s, "\n") {
					out.WriteString("\n")
				}
				out.WriteString(string(p.Bytes(l.Text)) + eol)
			}
		}
		cursor = before + int(count)
	}
	for ; cursor < len(base); cursor++ {
		out.WriteString(base[cursor])
	}
	return out.String()
}

// TestSelectPastNoNewline tries every choice of lines of edits next to a
// last line with no newline, both ways, through git apply: a line that
// ended a side and has a chosen line after it gains a newline instead of
// joining it.
func TestSelectPastNoNewline(t *testing.T) {
	r := gittest.Init(t)
	rn := testrun.Runner(t)
	apply := func(patch []byte, args ...string) error {
		cmd := exec.Command(rn.Git().Path, append([]string{"apply", "--whitespace=nowarn"}, args...)...)
		cmd.Dir, cmd.Env, cmd.Stdin = r.Dir, gittest.Env(), bytes.NewReader(patch)
		if out, err := cmd.CombinedOutput(); err != nil {
			return errors.New(string(out))
		}
		return nil
	}
	for _, c := range []struct{ old, nu string }{
		{"a\nb", "a\nb\nc\nd\n"},
		{"a\nb", "a\nc"},
		{"a\nb\n", "a\nc"},
		{"a\nb", "a\nb\n"},
		{"x", "y\nz"},
		{"a\nb\nc", "a\nc"},
		{"a\nb\nc\n", "a\nX\nc\nd"},
	} {
		r.Write("f", c.old)
		r.Git("add", "f")
		r.Write("f", c.nu)
		pt, err := WorkTreePatch(context.Background(), rn, r.GitDir(), r.Dir, PatchRequest{Source: Unstaged, Context: 3, ForApply: true})
		if err != nil {
			t.Fatal(err)
		}
		p, err := Parse(pt.Text)
		if err != nil {
			t.Fatal(err)
		}
		var changed []int
		for i, l := range p.Lines {
			if l.Kind == '+' || l.Kind == '-' {
				changed = append(changed, i)
			}
		}
		for set := 1; set < 1<<len(changed); set++ {
			pick := map[int]bool{}
			for b, i := range changed {
				pick[i] = set&(1<<b) != 0
			}
			chosen := func(i int) bool { return pick[i] }
			for _, reverse := range []bool{false, true} {
				sel, err := p.Select(0, reverse, chosen)
				if err != nil {
					t.Fatal(err)
				}
				var got string
				if reverse {
					if err := apply(sel, "-R"); err != nil {
						t.Fatalf("%q -> %q, choice %b, discard: %s\n%s", c.old, c.nu, set, err, sel)
					}
					b, _ := os.ReadFile(filepath.Join(r.Dir, "f"))
					got = string(b)
					r.Write("f", c.nu)
				} else {
					if err := apply(sel, "--cached"); err != nil {
						t.Fatalf("%q -> %q, choice %b, stage: %s\n%s", c.old, c.nu, set, err, sel)
					}
					got = r.Git("show", ":f")
					r.Write("f", c.old)
					r.Git("add", "f")
					r.Write("f", c.nu)
				}
				if want := expect(p, c.old, c.nu, reverse, chosen); got != want {
					t.Fatalf("%q -> %q, choice %b, reverse %v: got %q, want %q\n%s", c.old, c.nu, set, reverse, got, want, sel)
				}
			}
		}
	}
}

// Lines staged or discarded leave the file's mode as it is on the side
// they go to.
func TestSelectLeavesTheModeAlone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no executable bit to change on Windows")
	}
	r := gittest.Init(t)
	rn := testrun.Runner(t)
	r.Write("f", "a\nb\n")
	r.Commit("one")
	path := filepath.Join(r.Dir, "f")
	r.Write("f", "a\nB\nc\n")
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	pt, err := WorkTreePatch(context.Background(), rn, r.GitDir(), r.Dir, PatchRequest{Source: Unstaged, Context: 3, ForApply: true})
	if err != nil {
		t.Fatal(err)
	}
	p, err := Parse(pt.Text)
	if err != nil || !bytes.Contains(pt.Text, []byte("new mode 100755")) {
		t.Fatalf("%v\n%s", err, pt.Text)
	}
	last := len(p.Lines) - 1 // +c
	only := func(i int) bool { return i == last }
	apply := func(patch []byte, args ...string) {
		t.Helper()
		cmd := exec.Command(rn.Git().Path, append([]string{"apply"}, args...)...)
		cmd.Dir, cmd.Env, cmd.Stdin = r.Dir, gittest.Env(), bytes.NewReader(patch)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s\n%s", out, patch)
		}
	}
	sel, err := p.Select(0, false, only)
	if err != nil {
		t.Fatal(err)
	}
	apply(sel, "--cached")
	if got := r.Git("ls-files", "-s", "f"); !strings.HasPrefix(got, "100644 ") || r.Git("show", ":f") != "a\nb\nc\n" {
		t.Fatalf("staging a line: %q %q", got, r.Git("show", ":f"))
	}
	r.Git("reset", "-q")
	sel, err = p.Select(0, true, only)
	if err != nil {
		t.Fatal(err)
	}
	apply(sel, "-R")
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm()&0o100 == 0 {
		t.Fatalf("discarding a line took the mode back: %v %v", st.Mode(), err)
	}
	if b, _ := os.ReadFile(path); string(b) != "a\nB\n" {
		t.Fatalf("discarding a line: %q", b)
	}
}

// Lines of a new file cannot be taken back one by one, nor lines of a
// deleted one taken: git applies those patches only whole.
func TestSelectNewAndDeletedFiles(t *testing.T) {
	r := gittest.Init(t)
	rn := testrun.Runner(t)
	r.Write("gone", "x\ny\n")
	r.Commit("one")
	r.Write("new", "a\nb\n")
	r.Git("add", "new")
	os.Remove(filepath.Join(r.Dir, "gone"))
	read := func(src Source, path string) *Parsed {
		t.Helper()
		pt, err := WorkTreePatch(context.Background(), rn, r.GitDir(), r.Dir, PatchRequest{Source: src, Paths: []string{path}, Context: 3, ForApply: true})
		if err != nil {
			t.Fatal(err)
		}
		p, err := Parse(pt.Text)
		if err != nil || len(p.Files) != 1 {
			t.Fatalf("%v\n%s", err, pt.Text)
		}
		return p
	}
	first := func(p *Parsed) func(int) bool {
		for i, l := range p.Lines {
			if l.Kind == '+' || l.Kind == '-' {
				return func(j int) bool { return j == i }
			}
		}
		return nil
	}
	every := func(int) bool { return true }
	added := read(Staged, "new")
	if added.Partial(0, true) != ErrNotPartial || added.Partial(0, false) != nil {
		t.Fatal("a new file's lines are offered to unstage one by one")
	}
	if _, err := added.Select(0, true, first(added)); err != ErrNotPartial {
		t.Fatalf("some lines of a new file to unstage: %v", err)
	}
	if _, err := added.Select(0, true, every); err != nil {
		t.Fatalf("all lines of a new file to unstage: %v", err)
	}
	deleted := read(Unstaged, "gone")
	if deleted.Partial(0, false) != ErrNotPartial || deleted.Partial(0, true) != nil {
		t.Fatal("a deleted file's lines are offered to stage one by one")
	}
	if _, err := deleted.Select(0, false, first(deleted)); err != ErrNotPartial {
		t.Fatalf("some lines of a deleted file to stage: %v", err)
	}
}

// git diff shows a file stored in Git LFS as its pointer. Some of a
// pointer's lines staged or discarded make no pointer: git-lfs then
// writes the pointer text into the work tree, or the index gets a broken
// one. Its lines go only all at once.
func TestSelectRefusesSomeLinesOfAnLFSPointer(t *testing.T) {
	oid := func(c string) string { return "oid sha256:" + strings.Repeat(c, 64) }
	head := "diff --git a/a.dat b/a.dat\nindex 1..2 100644\n--- a/a.dat\n+++ b/a.dat\n"
	for _, c := range []struct{ name, patch string }{
		{"changed", head + "@@ -1,3 +1,3 @@\n version https://git-lfs.github.com/spec/v1\n-" + oid("1") + "\n-size 4\n+" + oid("2") + "\n+size 11\n"},
		{"no context", head + "@@ -2,2 +2,2 @@\n-" + oid("1") + "\n-size 4\n+" + oid("2") + "\n+size 11\n"},
		{"moved to LFS", head + "@@ -1,2 +1,3 @@\n-one\n-two\n+version https://git-lfs.github.com/spec/v1\n+" + oid("2") + "\n+size 11\n"},
		{"moved out of LFS", head + "@@ -1,3 +1 @@\n-version https://git-lfs.github.com/spec/v1\n-" + oid("1") + "\n-size 4\n+one\n"},
	} {
		p, err := Parse([]byte(c.patch))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		var changed []int
		for i, l := range p.Lines {
			if l.Kind == '+' || l.Kind == '-' {
				changed = append(changed, i)
			}
		}
		for _, reverse := range []bool{false, true} {
			if err := p.Partial(0, reverse); err != ErrNotPartial {
				t.Errorf("%s, reverse %v: lines offered one by one: %v", c.name, reverse, err)
			}
			for _, one := range changed {
				if sel, err := p.Select(0, reverse, func(i int) bool { return i == one }); err != ErrNotPartial {
					t.Errorf("%s, reverse %v: line %d alone: %v\n%s", c.name, reverse, one, err, sel)
				}
			}
			if _, err := p.Select(0, reverse, func(int) bool { return true }); err != nil {
				t.Errorf("%s, reverse %v: every line: %v", c.name, reverse, err)
			}
		}
	}
	// The patch git diff gives with git-lfs installed, staged and not.
	if _, err := exec.LookPath("git-lfs"); err != nil {
		t.Log("no git-lfs:", err)
	} else {
		r := gittest.Init(t)
		rn := testrun.Runner(t)
		r.Git("config", "filter.lfs.clean", "git-lfs clean -- %f")
		r.Git("config", "filter.lfs.smudge", "git-lfs smudge -- %f")
		r.Git("config", "filter.lfs.process", "git-lfs filter-process")
		r.Git("config", "filter.lfs.required", "true")
		r.Write(".gitattributes", "*.dat filter=lfs diff=lfs merge=lfs -text\n")
		r.Write("a.dat", "one\n")
		r.Commit("one")
		r.Write("a.dat", "two-longer\n")
		r.Git("add", "a.dat")
		r.Write("a.dat", "three\n")
		for _, src := range []Source{Unstaged, Staged} {
			pt, err := WorkTreePatch(context.Background(), rn, r.GitDir(), r.Dir, PatchRequest{Source: src, Paths: []string{"a.dat"}, Context: -1, ForApply: true})
			if err != nil {
				t.Fatal(err)
			}
			p, err := Parse(pt.Text)
			if err != nil || len(p.Files) != 1 || !bytes.Contains(pt.Text, []byte("+oid sha256:")) {
				t.Fatalf("%v\n%s", err, pt.Text)
			}
			if p.Partial(0, false) != ErrNotPartial || p.Partial(0, true) != ErrNotPartial {
				t.Errorf("source %d: the lines of an LFS pointer are offered one by one", src)
			}
		}
	}
	// A file that merely mentions the version line, past its first line,
	// is text like any other.
	p, err := Parse([]byte(head + "@@ -4,2 +4,2 @@ notes\n version https://git-lfs.github.com/spec/v1\n-old\n+new\n"))
	if err != nil || p.Partial(0, false) != nil || p.Partial(0, true) != nil {
		t.Fatalf("a text file mentioning a pointer: %v", err)
	}
}

func splitLines(s string) []string {
	l := strings.SplitAfter(s, "\n")
	if l[len(l)-1] == "" {
		l = l[:len(l)-1]
	}
	return l
}

func TestForApplyIgnoresUserConfig(t *testing.T) {
	r := gittest.Init(t)
	rn := testrun.Runner(t)
	r.Write("f.txt", "a\n\nb\n\nc\n")
	r.Write(".gitattributes", "*.txt diff=upper\n")
	r.Commit("one")
	r.Git("config", "diff.noprefix", "true")
	r.Git("config", "diff.suppressBlankEmpty", "true")
	r.Git("config", "diff.upper.textconv", "tr a-z A-Z <")
	r.Write("f.txt", "a\n\nB\n\nc\n")
	pt, err := WorkTreePatch(context.Background(), rn, r.GitDir(), r.Dir, PatchRequest{Source: Unstaged, Context: -1, ForApply: true})
	if err != nil {
		t.Fatal(err)
	}
	p, err := Parse(pt.Text)
	if err != nil || len(p.Files) != 1 || p.Path(0) != "f.txt" {
		t.Fatalf("%v %s", err, pt.Text)
	}
	sel, err := p.Select(0, false, func(int) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(rn.Git().Path, "apply", "--cached", "-")
	cmd.Dir, cmd.Env, cmd.Stdin = r.Dir, gittest.Env(), bytes.NewReader(sel)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s\n%s", out, sel)
	}
	if got := r.Git("show", ":f.txt"); got != "a\n\nB\n\nc\n" {
		t.Fatalf("index has %q", got)
	}
}
