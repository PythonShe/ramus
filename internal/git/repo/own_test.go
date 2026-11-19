package repo

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/run"
	"ramus/internal/git/trust"
)

// A folder whose .git file, or whose git directory's commondir, names a
// trusted repository opens Restricted: it is not that repository, and
// the trusted config's hooks must not run from the folder's files. A
// worktree git registered, a submodule and the repository itself keep
// the trust; a folder the user trusts on its own opens trusted.
func TestTrustIsTheFoldersOwn(t *testing.T) {
	ctx := context.Background()
	runner := testrun.Runner(t)
	store, err := trust.Load(filepath.Join(t.TempDir(), "trust.json"))
	if err != nil {
		t.Fatal(err)
	}
	trusted := func(l Location) bool { return store.Trusted(l.CommonDir) }
	open := func(dir string) *Repository {
		t.Helper()
		r, err := Open(ctx, runner, dir, trusted)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		t.Cleanup(r.Close)
		return r
	}

	a := gittest.Init(t)
	a.Write("f", "x\n")
	a.Commit("one")
	a.Git("config", "core.hooksPath", ".husky")
	if err := store.Set(a.GitDir(), true); err != nil {
		t.Fatal(err)
	}
	if r := open(a.Dir); r.Restricted || r.TrustDir != resolve(a.GitDir()) {
		t.Fatalf("the trusted repository: restricted %v, trust dir %s", r.Restricted, r.TrustDir)
	}

	// A folder whose .git file names the trusted repository, carrying the
	// hook the trusted config's hooksPath finds in it.
	evil := t.TempDir()
	os.WriteFile(filepath.Join(evil, ".git"), []byte("gitdir: "+a.GitDir()+"\n"), 0o644)
	marker := filepath.Join(t.TempDir(), "ran")
	os.MkdirAll(filepath.Join(evil, ".husky"), 0o755)
	os.WriteFile(filepath.Join(evil, ".husky", "pre-commit"), []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o755)
	r := open(evil)
	if !r.Restricted {
		t.Fatal("a folder naming a trusted repository in its .git file opened trusted")
	}
	r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, WorkTree: r.WorkTree, Args: []string{"commit", "-q", "--allow-empty", "-m", "x"}})
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the folder's hook ran")
	}

	// A git directory of the folder's own whose commondir names it.
	evil2 := t.TempDir()
	fake := filepath.Join(evil2, "fake")
	os.MkdirAll(fake, 0o755)
	os.WriteFile(filepath.Join(evil2, ".git"), []byte("gitdir: fake\n"), 0o644)
	os.WriteFile(filepath.Join(fake, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644)
	os.WriteFile(filepath.Join(fake, "commondir"), []byte(a.GitDir()+"\n"), 0o644)
	if r := open(evil2); !r.Restricted {
		t.Fatal("a folder whose commondir names a trusted repository opened trusted")
	}

	// A folder naming a worktree git registered elsewhere.
	wt := filepath.Join(t.TempDir(), "wt")
	a.Git("worktree", "add", "-q", wt)
	if r := open(wt); r.Restricted {
		t.Fatal("a worktree of the trusted repository opened Restricted")
	}
	evil3 := t.TempDir()
	wtGit := open(wt).GitDir
	os.WriteFile(filepath.Join(evil3, ".git"), []byte("gitdir: "+wtGit+"\n"), 0o644)
	if r := open(evil3); !r.Restricted {
		t.Fatal("a folder naming a trusted repository's worktree opened trusted")
	}

	// A submodule: its git directory is in the superproject's modules/,
	// and its config names its work tree.
	sub := gittest.Init(t)
	sub.Write("s", "x\n")
	sub.Commit("one")
	a.Git("-c", "protocol.file.allow=always", "submodule", "add", "-q", sub.Dir, "sub")
	sr := open(filepath.Join(a.Dir, "sub"))
	if !sr.Submodule || sr.TrustDir != sr.CommonDir {
		t.Fatalf("the submodule's trust is not its git directory's: %+v %s", sr.Location, sr.TrustDir)
	}

	// The user trusts a folder of their own apart from its git directory
	// (git init --separate-git-dir): it opens trusted, as that folder.
	if err := store.Set(r.TrustDir, true); err != nil {
		t.Fatal(err)
	}
	if r := open(evil); r.Restricted {
		t.Fatal("a folder the user trusted opened Restricted")
	}
}
