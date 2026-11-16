package lfs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/lfsserver"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/run"
)

func TestUses(t *testing.T) {
	dir := t.TempDir()
	git := filepath.Join(dir, ".git")
	os.Mkdir(git, 0o755)
	if Uses(git, dir) {
		t.Fatal("a repository without LFS uses it")
	}
	os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("# *.psd filter=lfs\n*.txt text\n"), 0o644)
	if Uses(git, dir) {
		t.Fatal("a comment counts")
	}
	os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("*.psd filter=lfs diff=lfs merge=lfs -text lockable\n"), 0o644)
	if !Uses(git, dir) {
		t.Fatal("an LFS filter does not count")
	}
	os.Remove(filepath.Join(dir, ".gitattributes"))
	os.Mkdir(filepath.Join(git, "lfs"), 0o755)
	if !Uses(git, "") {
		t.Fatal("stored LFS objects do not count")
	}
}

// The locks list as the server keeps them, the user's told from the
// others' when the server verifies them, and without when it cannot.
func TestList(t *testing.T) {
	if _, err := exec.LookPath("git-lfs"); err != nil {
		t.Skip("no git-lfs:", err)
	}
	srv := lfsserver.New(t, "me")
	srv.Add("b.psd", "alice")
	srv.Add("a.psd", "me")
	r := gittest.Init(t)
	r.Git("config", "lfs.url", srv.URL)
	runner := testrun.Runner(t)

	ls, err := List(context.Background(), runner, r.GitDir(), r.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if !ls.Verified || len(ls.List) != 2 {
		t.Fatalf("got %+v", ls)
	}
	a, b := ls.List[0], ls.List[1]
	if a.Path != "a.psd" || !a.Ours || a.Owner != "me" || a.ID == "" || a.At.IsZero() {
		t.Errorf("a.psd: %+v", a)
	}
	if b.Path != "b.psd" || b.Ours || b.Owner != "alice" {
		t.Errorf("b.psd: %+v", b)
	}

	srv.NoVerify.Store(true)
	ls, err = List(context.Background(), runner, r.GitDir(), r.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if ls.Verified || len(ls.List) != 2 || ls.List[0].Ours {
		t.Fatalf("unverified: %+v", ls)
	}
}

// git without git-lfs fails with a message the app says in its own words.
func TestMissing(t *testing.T) {
	err := &run.Error{ExitCode: 1, Stderr: "git: 'lfs' is not a git command. See 'git --help'.\n"}
	if !missing(run.Result{}, err) {
		t.Error("git without lfs not told")
	}
	if missing(run.Result{}, &run.Error{ExitCode: 2, Stderr: "Locking support not enabled"}) {
		t.Error("another failure taken for no lfs")
	}
}
