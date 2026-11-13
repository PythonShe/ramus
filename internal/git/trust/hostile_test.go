package trust

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ramus/internal/git/catfile"
	"ramus/internal/git/commits"
	"ramus/internal/git/config"
	"ramus/internal/git/diff"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/history"
	"ramus/internal/git/run"
)

var hooks = []string{"pre-commit", "post-checkout", "post-index-change", "reference-transaction",
	"fsmonitor-watchman", "post-commit", "pre-auto-gc", "post-rewrite"}

// hostile builds a repository whose own config runs a program wherever git
// lets config run one; each program leaves a file in marks.
func hostile(t *testing.T) (r *gittest.Repo, marks string) {
	if runtime.GOOS == "windows" {
		t.Skip("the fixture's programs are shell commands")
	}
	marks = t.TempDir()
	r = gittest.Init(t)
	r.Write(".gitattributes", "*.x filter=evil diff=evil merge=evil\n")
	r.Write("a.x", "one\n")
	r.Write("b", "plain\n")
	r.Commit("one")
	// A signed commit, so that showing signatures would start gpg.
	body := r.Git("cat-file", "commit", "HEAD")
	tree, rest, _ := strings.Cut(body, "\n")
	signed := tree + "\n" + strings.Replace(rest, "\n\n", "\ngpgsig -----BEGIN PGP SIGNATURE-----\n \n -----END PGP SIGNATURE-----\n\n", 1)
	id, err := r.Run([]byte(signed), "hash-object", "-t", "commit", "-w", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	r.Git("update-ref", "refs/heads/main", strings.TrimSpace(id))

	mark := func(name string) string { return fmt.Sprintf("touch %q; ", filepath.Join(marks, name)) }
	hookDir := filepath.Join(r.Dir, ".githooks")
	for _, dir := range []string{hookDir, filepath.Join(r.GitDir(), "hooks")} {
		os.MkdirAll(dir, 0o755)
		for _, h := range hooks {
			os.WriteFile(filepath.Join(dir, h), []byte("#!/bin/sh\n"+mark("hook-"+h)+"\n"), 0o755)
		}
	}
	for k, v := range map[string]string{
		"core.fsmonitor":            mark("fsmonitor") + "exit 1",
		"core.hooksPath":            hookDir,
		"core.pager":                mark("pager") + "cat",
		"filter.evil.clean":         mark("clean") + "cat",
		"filter.evil.smudge":        mark("smudge") + "cat",
		"filter.evil.process":       mark("process") + "exit 1",
		"filter.evil.required":      "true",
		"diff.evil.textconv":        mark("textconv") + "cat",
		"diff.evil.command":         mark("diffcommand"),
		"diff.external":             mark("external"),
		"merge.evil.driver":         mark("merge"),
		"gpg.program":               mark("gpg") + "exit 1",
		"gpg.ssh.program":           mark("gpgssh") + "exit 1",
		"log.showSignature":         "true",
		"credential.helper":         "!" + mark("credential"),
		"core.sshCommand":           mark("ssh"),
		"core.alternateRefsCommand": mark("alternates"),
	} {
		r.Git("config", k, v)
	}
	// Changed, and with a stat that makes status rehash it through the
	// clean filter.
	r.Write("a.x", "two\n")
	r.Write("new.x", "new\n")
	return r, marks
}

func ran(marks string) []string {
	es, _ := os.ReadDir(marks)
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

// reads runs what the app does in a repository it only reads.
// A trusted run of the fixture fails where its programs fail; strict is
// off for it.
func reads(t *testing.T, runner *run.Runner, r *gittest.Repo, strict bool) {
	ctx := context.Background()
	check := func(what any, err error) {
		t.Helper()
		if err != nil && strict {
			t.Fatalf("%v: %v", what, err)
		}
	}
	gd := r.GitDir()
	_, err := config.Read(ctx, runner, gd)
	check("config", err)
	for _, args := range [][]string{
		{"status", "--porcelain=v2", "-z", "--branch", "--show-stash"},
		{"log", "-3", "--format=%H %G? %s"},
		{"show", "HEAD"},
		{"log", "-p", "-1"},
		{"diff", "HEAD"},
		{"for-each-ref"},
	} {
		_, err := runner.Run(ctx, &run.Cmd{GitDir: gd, WorkTree: r.Dir, Args: args, Read: true})
		check(args, err)
	}
	for _, src := range []diff.Source{diff.Unstaged, diff.Staged} {
		_, err := diff.WorkTreePatch(ctx, runner, gd, r.Dir, diff.PatchRequest{Source: src, Context: -1})
		check(src, err)
	}
	_, err = diff.WorkTreePatch(ctx, runner, gd, r.Dir, diff.PatchRequest{Source: diff.Untracked, Paths: []string{"new.x"}, Context: -1})
	check("untracked", err)
	head, _ := hex.DecodeString(strings.TrimSpace(r.Git("rev-parse", "HEAD")))
	_, err = diff.CommitPatch(ctx, runner, gd, head, head, nil, -1, 0)
	check("commit patch", err)
	s := commits.New(20)
	check("rev-list", history.RevList(ctx, runner, gd, [][]byte{head}, s, func(uint32, []uint32) error { return nil }))
	p, err := catfile.Start(ctx, runner, gd)
	check("cat-file", err)
	if err != nil {
		return
	}
	p.Read([][]byte{head}, func(int, catfile.Object) error { return nil })
	p.Close()
}

// Nothing the repository's config names runs while the app reads it
// untrusted, and git refuses to write.
func TestHostileConfig(t *testing.T) {
	r, marks := hostile(t)
	base := testrun.Runner(t)
	cfg, err := config.Read(context.Background(), base, r.GitDir())
	if err != nil {
		t.Fatal(err)
	}
	hard := base.Hardened(Hardening(cfg))
	reads(t, hard, r, true)
	if got := ran(marks); len(got) != 0 {
		t.Fatalf("ran from the repository's config: %v", got)
	}
	_, err = hard.Run(context.Background(), &run.Cmd{GitDir: r.GitDir(), WorkTree: r.Dir, Args: []string{"commit", "-qam", "x"}})
	if !errors.Is(err, run.ErrRestricted) {
		t.Fatalf("a write ran: %v", err)
	}

	// The fixture is hostile: trusted, the same reads run its programs.
	reads(t, base, r, false)
	got := strings.Join(ran(marks), " ")
	for _, want := range []string{"fsmonitor", "process"} {
		if !strings.Contains(got, want) {
			t.Errorf("the fixture did not run %s when trusted (ran %s)", want, got)
		}
	}
}

func TestHardeningNamesLocalDrivers(t *testing.T) {
	cfg, err := config.Parse([]byte(
		"global\x00file:/g\x00filter.lfs.clean\ngit-lfs clean\x00" +
			"local\x00file:.git/config\x00filter.My.Odd.clean\nx\x00" +
			"local\x00file:.git/config\x00gpg.program\nx\x00" +
			"worktree\x00file:.git/config.worktree\x00diff.w.textconv\nx\x00"))
	if err != nil {
		t.Fatal(err)
	}
	h := strings.Join(Hardening(cfg), "\n")
	for _, want := range []string{"filter.My.Odd.clean=", "filter.My.Odd.process=", "gpg.program=", "diff.w.textconv=", "core.fsmonitor=false"} {
		if !strings.Contains(h, want+"\n") && !strings.HasSuffix(h, want) {
			t.Errorf("missing %q in\n%s", want, h)
		}
	}
	if strings.Contains(h, "lfs") {
		t.Error("overrode a global driver")
	}
}
