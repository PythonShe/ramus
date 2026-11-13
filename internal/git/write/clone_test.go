package write

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
