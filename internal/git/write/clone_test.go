package write

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/redact"
	"ramus/internal/git/run"
)

func TestCloneAndInit(t *testing.T) {
	rn := testrun.Runner(t)
	src := gittest.Init(t)
	src.Write("a", "1")
	src.Commit("one")
	sub := gittest.Init(t)
	sub.Commit("sub")
	src.Git("-c", "protocol.file.allow=always", "submodule", "add", "-q", sub.Dir, "sub")
	src.Commit("with a submodule")

	dir := filepath.Join(t.TempDir(), "new", "clone")
	j, err := Clone(rn, CloneOptions{URL: "file://" + src.Dir, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	progress := false
	for ev := range j.Events() {
		progress = progress || ev.Percent >= 0
	}
	if _, err := wait(t, j); err != nil {
		t.Fatal(err)
	}
	if !progress {
		t.Error("no progress events")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a")); string(b) != "1" {
		t.Fatal("not cloned")
	}
	if ents, _ := os.ReadDir(filepath.Join(dir, "sub")); len(ents) != 0 {
		t.Fatal("clone recursed into the submodule")
	}

	if _, err := Clone(rn, CloneOptions{URL: "file://" + src.Dir, Dir: dir}); !errors.Is(err, ErrDirNotEmpty) {
		t.Fatalf("clone into a full folder: %v", err)
	}
	if _, err := Clone(rn, CloneOptions{URL: "-uhoh", Dir: t.TempDir()}); !errors.Is(err, redact.ErrBadURL) {
		t.Fatalf("option as URL: %v", err)
	}
	j, _ = Clone(rn, CloneOptions{URL: "file://" + filepath.Join(t.TempDir(), "missing"), Dir: filepath.Join(t.TempDir(), "x")})
	if _, err := wait(t, j); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("clone of nothing: %v", err)
	}

	fresh := filepath.Join(t.TempDir(), "fresh")
	if err := Init(context.Background(), rn, fresh, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(fresh, ".git", "HEAD")); err != nil {
		t.Fatal("init made no repository")
	}
}

func TestCloneSubmodulesWhenAsked(t *testing.T) {
	g := testrun.Runner(t).Git()
	global := filepath.Join(t.TempDir(), "gitconfig")
	// git refuses file:// submodules unless told; only this test allows it.
	if err := os.WriteFile(global, []byte("[protocol \"file\"]\n\tallow = always\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rn := run.New(g, append(gittest.Env(), "GIT_CONFIG_GLOBAL="+global), 4)
	src := gittest.Init(t)
	sub := gittest.Init(t)
	sub.Write("inside", "x")
	sub.Commit("sub")
	src.Git("-c", "protocol.file.allow=always", "submodule", "add", "-q", sub.Dir, "sub")
	src.Commit("with a submodule")
	dir := filepath.Join(t.TempDir(), "clone")
	j, err := Clone(rn, CloneOptions{URL: "file://" + src.Dir, Dir: dir, Submodules: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wait(t, j); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sub", "inside")); err != nil {
		t.Fatal("submodule not cloned when asked")
	}
}

// A repository with files in Git LFS clones with their content, git-lfs
// running as git's filter, its progress among the events. Needs git-lfs.
func TestCloneLFS(t *testing.T) {
	if _, err := exec.LookPath("git-lfs"); err != nil {
		t.Skip("no git-lfs:", err)
	}
	g := testrun.Runner(t).Git()
	global := filepath.Join(t.TempDir(), "gitconfig")
	// What git lfs install writes to the user's config.
	lfs := "[filter \"lfs\"]\n\tclean = git-lfs clean -- %f\n\tsmudge = git-lfs smudge -- %f\n\tprocess = git-lfs filter-process\n\trequired = true\n"
	if err := os.WriteFile(global, []byte(lfs), 0o644); err != nil {
		t.Fatal(err)
	}
	env := append(gittest.Env(), "GIT_CONFIG_GLOBAL="+global)
	src := gittest.Init(t)
	src.Env = append(src.Env, "GIT_CONFIG_GLOBAL="+global)
	src.Write(".gitattributes", "*.bin filter=lfs diff=lfs merge=lfs -text\n")
	src.Write("big.bin", "large content\n")
	src.Git("add", ".gitattributes", "big.bin")
	src.Commit("lfs")
	if p := src.Git("cat-file", "-p", "HEAD:big.bin"); !strings.HasPrefix(p, "version https://git-lfs") {
		t.Fatalf("not a pointer: %q", p)
	}

	dir := filepath.Join(t.TempDir(), "clone")
	j, err := Clone(run.New(g, env, 4), CloneOptions{URL: "file://" + filepath.ToSlash(src.Dir), Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for ev := range j.Events() {
		lines = append(lines, ev.Line)
	}
	if _, err := wait(t, j); err != nil {
		t.Fatalf("%v\n%s", err, strings.Join(lines, "\n"))
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "big.bin")); string(b) != "large content\n" {
		t.Fatalf("big.bin holds %q", b)
	}
	if !slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, "Filtering content") || strings.Contains(l, "LFS") }) {
		t.Errorf("no LFS progress among\n%s", strings.Join(lines, "\n"))
	}
}
