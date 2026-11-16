package write

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// conflicted makes a merge of topic into main stop on a.txt and b.bin.
func conflicted(t *testing.T) *harness {
	h := newHarness(t)
	h.r.Write("a.txt", "one\n")
	h.r.Write("b.bin", "\x00base")
	h.r.Write("c.txt", "c\n")
	h.r.Commit("base")
	h.r.Git("switch", "-q", "-c", "topic")
	h.r.Write("a.txt", "theirs\n")
	h.r.Write("b.bin", "\x00theirs")
	h.r.Git("rm", "-q", "c.txt")
	h.r.Commit("topic")
	h.r.Git("switch", "-q", "main")
	h.r.Write("a.txt", "ours\n")
	h.r.Write("b.bin", "\x00ours")
	h.r.Write("c.txt", "c changed\n")
	h.r.Commit("main")
	if out, err := h.r.Run(nil, "merge", "topic"); err == nil {
		t.Fatalf("merged cleanly: %s", out)
	}
	return h
}

func TestResolveConflicts(t *testing.T) {
	h := conflicted(t)
	op, err := TakeSide(h.w, []string{"b.bin"}, TheirSide)
	h.do(op, err, true)
	if h.read("b.bin") != "\x00theirs" {
		t.Fatalf("b.bin %q", h.read("b.bin"))
	}
	op, err = RemoveConflicted(h.w, []string{"c.txt"})
	h.do(op, err, true)

	seen, err := Observe(h.w.WorkTree, []string{"a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	// A file changed since it was read is not overwritten.
	h.r.Write("a.txt", h.read("a.txt")+"edited\n")
	op, err = Resolve(h.w, "a.txt", []byte("merged\n"), seen[0])
	h.do(op, err, false)
	seen, _ = Observe(h.w.WorkTree, []string{"a.txt"})
	op, err = Resolve(h.w, "a.txt", []byte("merged\n"), seen[0])
	h.do(op, err, true)
	if h.read("a.txt") != "merged\n" || strings.Contains(h.r.Git("ls-files", "-u"), "a.txt") {
		t.Fatalf("a.txt %q, unmerged %q", h.read("a.txt"), h.r.Git("ls-files", "-u"))
	}
	if _, err := Resolve(h.w, "../out", nil, seen[0]); err == nil {
		// Refused when it runs, if not before.
		op, _ := Resolve(h.w, "../out", []byte("x"), FileState{Path: "../out"})
		h.do(op, nil, false)
	}

	op, err = Sequence(h.w, "merge", Continue)
	h.do(op, err, true)
	if parents := strings.Fields(h.r.Git("log", "-1", "--format=%P")); len(parents) != 2 {
		t.Fatalf("no merge commit: %v", parents)
	}
	if _, err := Sequence(h.w, "merge", Skip); err == nil {
		t.Fatal("a merge skipped")
	}
	if _, err := Sequence(h.w, "--exec", Continue); err == nil {
		t.Fatal("an option taken as an operation")
	}
}

func TestAbortSkipAndMergeTool(t *testing.T) {
	h := conflicted(t)
	op, err := Sequence(h.w, "merge", Abort)
	h.do(op, err, true)
	if _, err := os.Stat(filepath.Join(h.r.GitDir(), "MERGE_HEAD")); !os.IsNotExist(err) {
		t.Fatal("still merging")
	}

	// A rebase that stops can skip the commit.
	h.r.Git("switch", "-q", "topic")
	if _, err := h.r.Run(nil, "rebase", "main"); err == nil {
		t.Fatal("rebased cleanly")
	}
	op, err = Sequence(h.w, "rebase", Skip)
	h.do(op, err, true)
	if _, err := os.Stat(filepath.Join(h.r.GitDir(), "rebase-merge")); !os.IsNotExist(err) {
		t.Fatal("still rebasing")
	}

}

// Continuing a rebase, a cherry-pick, a revert or a merge stopped on a
// conflict keeps the lines of the message that start with #, and leaves
// out the conflicts git listed in it, whatever the comment character and
// commit.cleanup.
func TestContinueKeepsTheMessage(t *testing.T) {
	const msg = "side subject\n\n# kept hash line\n; kept semicolon line\nbody\n"
	for _, c := range []struct{ operation, config string }{
		{"cherry-pick", ""}, {"rebase", ""}, {"revert", ""}, {"merge", ""},
		{"cherry-pick", "core.commentChar=;"}, {"rebase", "commit.cleanup=scissors"},
	} {
		t.Run(c.operation+" "+c.config, func(t *testing.T) {
			h := newHarness(t)
			if c.config != "" {
				k, v, _ := strings.Cut(c.config, "=")
				h.r.Git("config", k, v)
			}
			h.r.Write("f", "1\n")
			h.r.Commit("base")
			h.r.Git("switch", "-q", "-c", "side")
			h.r.Write("f", "side\n")
			h.r.Git("commit", "-q", "-a", "--cleanup=verbatim", "-m", msg)
			h.r.Git("switch", "-q", "main")
			h.r.Write("f", "main\n")
			h.r.Commit("main")
			want := msg
			var stop []string
			switch c.operation {
			case "cherry-pick":
				stop = []string{"cherry-pick", "side"}
			case "rebase":
				h.r.Git("switch", "-q", "side")
				stop = []string{"rebase", "main"}
			case "revert":
				h.r.Git("reset", "-q", "--hard", "side")
				h.r.Write("f", "after\n")
				h.r.Commit("after")
				stop = []string{"revert", "--no-edit", "side"}
				want = ""
			case "merge":
				h.r.Git("config", "merge.log", "false")
				stop = []string{"merge", "--no-edit", "side"}
				want = ""
			}
			if out, err := h.r.Run(nil, stop...); err == nil {
				t.Fatalf("no conflict: %s", out)
			}
			if want == "" {
				// The message git prepared, its conflicts left out, with a
				// line of the user's that starts with #.
				b, err := os.ReadFile(filepath.Join(h.r.GitDir(), "MERGE_MSG"))
				if err != nil {
					t.Fatal(err)
				}
				want = dropConflicts(string(b)) + "\n# a line of mine\n"
				if err := os.WriteFile(filepath.Join(h.r.GitDir(), "MERGE_MSG"), []byte(want+"\n# Conflicts:\n#\tf\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(want, "Conflicts") {
					t.Fatalf("conflicts left in %q", want)
				}
			}
			h.r.Write("f", "resolved\n")
			h.r.Git("add", "f")
			op, err := Sequence(h.w, c.operation, Continue)
			h.do(op, err, true)
			if got := h.r.Git("log", "-1", "--format=%B", "HEAD"); strings.TrimRight(got, "\n") != strings.TrimRight(want, "\n") {
				t.Fatalf("message %q, want %q", got, want)
			}
		})
	}
}

func TestDropConflicts(t *testing.T) {
	for in, want := range map[string]string{
		"s\n\n# x\nb\n\n# Conflicts:\n#\tf\n#\tg h\n": "s\n\n# x\nb\n",
		"s\n\n; Conflicts:\n;\tf\n":                   "s\n",
		"s\n\n# ------------------------ >8 ------------------------\n# Do not modify or remove the line above.\n# Everything below it will be ignored.\n#\n# Conflicts:\n#\tf\n": "s\n",
		"s\n\n# Conflicts:\nnot a path\n": "s\n\n# Conflicts:\nnot a path\n",
		"s\n\nbody\n":                     "s\n\nbody\n",
	} {
		if got := dropConflicts(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

// The merge tool from the config writes the result and git marks it
// resolved.
func TestMergeTool(t *testing.T) {
	h := conflicted(t)
	h.r.Git("config", "mergetool.fake.cmd", `printf 'from tool\n' > "$MERGED"`)
	h.r.Git("config", "mergetool.fake.trustExitCode", "true")
	h.r.Git("config", "merge.tool", "fake")
	op, err := MergeTool(h.w, []string{"a.txt"}, false)
	h.do(op, err, true)
	if h.read("a.txt") != "from tool\n" || strings.Contains(h.r.Git("ls-files", "-u"), "a.txt") {
		t.Fatalf("a.txt %q, unmerged %q", h.read("a.txt"), h.r.Git("ls-files", "-u"))
	}
}
