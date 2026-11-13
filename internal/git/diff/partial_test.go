package diff

import (
	"bytes"
	"context"
	"errors"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
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

// TestSelectAgainstGit stages and discards random choices of lines of
// random edits through git apply, and checks the result against what the
// choice means, line by line.
func TestSelectAgainstGit(t *testing.T) {
	r := gittest.Init(t)
	rn := testrun.Runner(t)
	rng := rand.New(rand.NewPCG(1, 2))
	words := []string{"alpha", "beta", "", "gamma", "delta", "beta", "  indented", "}"}
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
	tried := 0
	for iter := range 60 {
		old := text(rng.IntN(30), rng.IntN(4) != 0)
		nu := edit(old)
		if nu == old {
			continue
		}
		r.Write("f", old)
		r.Git("add", "f")
		r.Write("f", nu)
		ctxLines := 1 + rng.IntN(3)
		pt, err := WorkTreePatch(context.Background(), rn, r.GitDir(), r.Dir, PatchRequest{Source: Unstaged, Context: ctxLines, ForApply: true})
		if err != nil {
			t.Fatal(err)
		}
		p, err := Parse(pt.Text)
		if err != nil {
			t.Fatalf("iter %d: %v\n%s", iter, err, pt.Text)
		}
		pick := map[int]bool{}
		for i, l := range p.Lines {
			if (l.Kind == '+' || l.Kind == '-') && rng.IntN(2) == 0 {
				pick[i] = true
			}
		}
		chosen := func(i int) bool { return pick[i] }
		for _, reverse := range []bool{false, true} {
			sel, err := p.Select(0, reverse, chosen)
			if errors.Is(err, ErrNothingChosen) {
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			want := expect(p, old, nu, reverse, chosen)
			var got string
			if reverse {
				// Discard the chosen lines from the work tree.
				if err := apply(sel, "-R"); err != nil {
					t.Fatalf("iter %d discard: %s\nold %q\nnew %q\npatch:\n%s\nselection:\n%s", iter, err, old, nu, pt.Text, sel)
				}
				b, _ := os.ReadFile(filepath.Join(r.Dir, "f"))
				got = string(b)
				r.Write("f", nu)
			} else {
				// Stage the chosen lines.
				if err := apply(sel, "--cached"); err != nil {
					t.Fatalf("iter %d stage: %s\nold %q\nnew %q\npatch:\n%s\nselection:\n%s", iter, err, old, nu, pt.Text, sel)
				}
				got = r.Git("show", ":f")
				r.Write("f", old)
				r.Git("add", "f")
				r.Write("f", nu)
			}
			if got != want {
				t.Fatalf("iter %d reverse=%v:\ngot  %q\nwant %q\nold %q\nnew %q\nselection:\n%s", iter, reverse, got, want, old, nu, sel)
			}
			tried++
		}
	}
	if tried < 80 {
		t.Fatalf("only %d selections tried", tried)
	}
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
