package write

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/run"
)

// winLimit is the longest command line Windows starts, in characters,
// less room for git's own path.
const winLimit = 32767 - 1024

// winLine is c's command line as Go hands it to Windows: each argument
// quoted as syscall.EscapeArg does, spaces between.
func winLine(c run.Cmd) int {
	args := []string{"--git-dir=" + c.GitDir, "--work-tree=" + c.WorkTree}
	for _, kv := range c.Config {
		args = append(args, "-c", kv)
	}
	n := 0
	for _, a := range append(args, c.Args...) {
		n += len(escapeArg(a)) + 1
	}
	return n
}

func escapeArg(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, "\"\\ \t") {
		return s
	}
	space := strings.ContainsAny(s, " \t")
	if !strings.ContainsAny(s, `"\`) {
		return `"` + s + `"`
	}
	var b strings.Builder
	if space {
		b.WriteByte('"')
	}
	slashes := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			slashes++
		case '"':
			b.WriteString(strings.Repeat(`\`, slashes+1))
			slashes = 0
		default:
			slashes = 0
		}
		b.WriteByte(s[i])
	}
	if space {
		b.WriteString(strings.Repeat(`\`, slashes) + `"`)
	}
	return b.String()
}

// commands is every command op runs, as built.
func commands(op Op) []run.Cmd {
	cmds := []run.Cmd{op.Cmd}
	if op.Pre != nil {
		cmds = append(cmds, *op.Pre)
	}
	return append(cmds, op.More...)
}

// Every command a write runs fits a Windows command line, however many
// paths it is given and however long they are; git reads them on its
// standard input where it can.
func TestPathListsFitACommandLine(t *testing.T) {
	h := newHarness(t)
	const n = 1000
	name := func(i int) string {
		return fmt.Sprintf("src/components/feature %04d/the feature's own \"index\".test.tsx", i)
	}
	if !gittest.Portable(name(0)) {
		name = func(i int) string {
			return fmt.Sprintf("src/components/feature %04d/the feature's own index.test.tsx", i)
		}
	}
	var paths []string
	for i := range n {
		paths = append(paths, name(i))
		h.r.Write(paths[i], "base\n")
	}
	h.r.Commit("base")
	h.r.Git("switch", "-q", "-c", "topic")
	for _, p := range paths {
		h.r.Write(p, "theirs\n")
	}
	h.r.Commit("topic")
	h.r.Git("switch", "-q", "main")
	for _, p := range paths {
		h.r.Write(p, "ours\n")
	}
	h.r.Commit("main")
	if out, err := h.r.Run(nil, "merge", "topic"); err == nil {
		t.Fatalf("merged cleanly: %s", out)
	}

	// The commands built, a submodule's included, which this repository
	// has none of.
	tool, err := MergeTool(h.w, paths, false)
	if err != nil {
		t.Fatal(err)
	}
	for what, op := range map[string]Op{"merge tool": tool, "submodule update": SubmoduleUpdate(h.w, paths, true, false)} {
		cmds := commands(op)
		var got []string
		for _, c := range cmds {
			if l := winLine(c); l > winLimit {
				t.Errorf("%s: a command line of %d characters", what, l)
			}
			got = append(got, c.Args[slices.Index(c.Args, "--")+1:]...)
		}
		if !slices.Equal(got, paths) {
			t.Errorf("%s names %d paths of %d", what, len(got), len(paths))
		}
	}

	// Taking a side and deleting run on the repository, every path
	// resolved, no command line long.
	before := len(h.q.r.Journal().For(h.r.GitDir()))
	op, err := TakeSide(h.w, paths[:n/2], TheirSide)
	h.do(op, err, true)
	op, err = RemoveConflicted(h.w, paths[n/2:])
	h.do(op, err, true)
	for _, rec := range h.q.r.Journal().For(h.r.GitDir())[before:] {
		if len(rec.Line) > 4<<10 {
			t.Errorf("a command line of %d characters: %.200s", len(rec.Line), rec.Line)
		}
	}
	if u := h.r.Git("ls-files", "-u"); u != "" {
		t.Fatalf("still conflicted: %.200s", u)
	}
	if got := h.read(paths[0]); got != "theirs\n" {
		t.Fatalf("%s is %q", paths[0], got)
	}

	// Cleaning ignored files runs in parts, each with -x, and deletes
	// them all.
	h.r.Write(".gitignore", "*.o\n")
	var ignored []string
	for i := range 3 * n {
		p := fmt.Sprintf("build/output of feature %04d/object file number %d.o", i%100, i)
		h.r.Write(p, "o\n")
		ignored = append(ignored, p)
	}
	seen, err := Observe(h.w.WorkTree, ignored)
	if err != nil {
		t.Fatal(err)
	}
	op, err = Clean(h.w, ignored, true, seen)
	if err != nil {
		t.Fatal(err)
	}
	cmds := commands(op)
	for _, c := range cmds {
		if l := winLine(c); l > winLimit || !slices.Contains(c.Args, "-x") {
			t.Fatalf("a clean of %d characters, -x %v", l, slices.Contains(c.Args, "-x"))
		}
	}
	h.do(op, nil, true)
	if left := h.r.Git("ls-files", "--others", "--ignored", "--exclude-standard"); left != "" {
		t.Fatalf("left after the clean: %.200s", left)
	}
	t.Logf("clean of %d ignored files: %d runs", len(ignored), len(cmds))
}

// A cherry-pick of as many commits as one takes reads them on standard
// input, in the order given.
func TestCherryPickManyCommits(t *testing.T) {
	if testing.Short() {
		t.Skip("picks 1,000 commits")
	}
	h := newHarness(t)
	h.r.Write("base", "1")
	base := h.r.Commit("base")
	var stream strings.Builder
	fmt.Fprintf(&stream, "reset refs/heads/side\nfrom %s\n", h.rev("main"))
	for i := range MaxPick {
		fmt.Fprintf(&stream, "commit refs/heads/side\nmark :%d\ncommitter T <t@example.com> %d +0000\ndata 7\nc %04d\nM 644 inline f%04d\ndata 2\n%d\n\n",
			i+1, 1700000000+i, i, i, i%10)
	}
	if out, err := h.r.Run([]byte(stream.String()), "fast-import", "--quiet"); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	commits := strings.Fields(h.r.Git("rev-list", "--reverse", "main..side"))
	op, err := CherryPick(h.w, commits, h.seen())
	if err != nil {
		t.Fatal(err)
	}
	if l := winLine(op.Cmd); l > winLimit {
		t.Fatalf("a command line of %d characters", l)
	}
	h.do(op, nil, true)
	if got := h.r.Git("log", "-2", "--format=%s"); got != "c 0999\nc 0998\n" {
		t.Fatalf("last picked %q", got)
	}
	if n := strings.Count(h.r.Git("rev-list", "main", "^"+base), "\n"); n != MaxPick {
		t.Fatalf("%d commits picked", n)
	}
}

// The budget counts what Windows quoting adds, so names full of spaces
// and quotes still fit.
func TestSplitArgsCountsQuoting(t *testing.T) {
	w := Where{GitDir: "/r/.git", WorkTree: "/r"}
	var paths []string
	for i := range 4000 {
		paths = append(paths, fmt.Sprintf(`"q" "q" "q" "q" "q" \"q\" %d`, i))
	}
	op, err := Delete(w, paths)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range commands(op) {
		if l := winLine(c); l > winLimit {
			t.Fatalf("a command line of %d characters", l)
		}
		got = append(got, c.Args[slices.Index(c.Args, "--")+1:]...)
	}
	if !slices.Equal(got, paths) {
		t.Fatalf("named %d paths of %d", len(got), len(paths))
	}
	if cmds := SplitArgs(w.cmd("x"), nil); len(cmds) != 0 {
		t.Fatalf("%d commands for no paths", len(cmds))
	}
}
