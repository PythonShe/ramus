package repo

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"ramus/internal/git/config"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

func caps(t *testing.T, r *gittest.Repo) Capabilities {
	t.Helper()
	loc, err := Discover(r.Dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Read(context.Background(), testrun.Runner(t), loc.GitDir)
	if err != nil {
		t.Fatal(err)
	}
	return Check(loc, cfg)
}

func TestCapabilities(t *testing.T) {
	plain := gittest.Init(t)
	plain.Write("a", "1")
	plain.Commit("one")
	c := caps(t, plain)
	if !c.NativeRefs() || !c.NativeIndex() || !c.NativeObjects() || !c.CommitGraphUsable() || len(c.Fallbacks()) != 0 {
		t.Fatalf("plain repository: %+v", c)
	}

	sha := gittest.Init(t, "--object-format=sha256")
	sha.Write("a", "1")
	sha.Commit("one")
	if c := caps(t, sha); c.HashSize != 32 || c.NativeObjects() || c.NativeIndex() {
		t.Fatalf("sha256: %+v", c)
	}

	rt := gittest.Init(t, "--ref-format=reftable")
	rt.Commit("one")
	if c := caps(t, rt); c.RefStorage != "reftable" || !c.NativeRefs() || len(c.Fallbacks()) != 0 {
		t.Fatalf("reftable: %+v", c)
	}

	partial := gittest.Init(t)
	partial.Write("a", "1")
	partial.Commit("one")
	clone := filepath.Join(t.TempDir(), "partial")
	partial.Git("-c", "uploadpack.allowFilter=true", "config", "uploadpack.allowFilter", "true")
	partial.Git("clone", "-q", "--filter=blob:none", "file://"+partial.Dir, clone)
	if c := caps(t, gittest.Open(t, clone)); !c.PartialClone || c.NativeObjects() {
		t.Fatalf("partial clone: %+v", c)
	}

	split := gittest.Init(t)
	split.Write("a", "1")
	split.Commit("one")
	split.Git("update-index", "--split-index")
	if c := caps(t, split); !c.SplitIndex || c.NativeIndex() {
		t.Fatalf("split index: %+v", c)
	}

	shallow := filepath.Join(t.TempDir(), "shallow")
	plain.Commit("two")
	plain.Git("clone", "-q", "--depth=1", "file://"+plain.Dir, shallow)
	if c := caps(t, gittest.Open(t, shallow)); !c.Shallow || c.CommitGraphUsable() {
		t.Fatalf("shallow: %+v", c)
	}

	repl := gittest.Init(t)
	a := repl.Commit("a")
	b := repl.Commit("b")
	repl.Git("replace", a, b)
	if c := caps(t, repl); !c.ReplaceRefs || c.CommitGraphUsable() || !slices.Contains(c.Fallbacks(), "replace refs") {
		t.Fatalf("replace refs: %+v", c)
	}

	// In reftable replace refs leave no file of their own.
	rtRepl := gittest.Init(t, "--ref-format=reftable")
	a = rtRepl.Commit("a")
	if c := caps(t, rtRepl); c.ReplaceRefs {
		t.Fatalf("reftable without replace refs: %+v", c)
	}
	b = rtRepl.Commit("b")
	rtRepl.Git("replace", a, b)
	if c := caps(t, rtRepl); !c.ReplaceRefs || c.CommitGraphUsable() {
		t.Fatalf("reftable replace refs: %+v", c)
	}
	rtRepl.Git("pack-refs", "--all")
	if c := caps(t, rtRepl); !c.ReplaceRefs {
		t.Fatalf("reftable replace refs compacted: %+v", c)
	}
	rtRepl.Git("replace", "-d", a)
	if c := caps(t, rtRepl); c.ReplaceRefs {
		t.Fatalf("reftable replace ref deleted: %+v", c)
	}

	unknown := gittest.Init(t)
	unknown.Git("config", "core.repositoryformatversion", "1")
	unknown.Git("config", "extensions.somethingNew", "true")
	if c := caps(t, unknown); len(c.Unknown) != 1 || c.NativeRefs() {
		t.Fatalf("unknown extension: %+v", c)
	}
}

// In format version 0 git ignores extensions it does not know, honours
// those older than version 1, and refuses a repository naming one that
// only version 1 allows; past version 1 it refuses the repository.
func TestFormatVersion(t *testing.T) {
	for _, tc := range []struct {
		version, ext, value string
		works               bool
	}{
		{"0", "somethingnew", "true", true},
		{"0", "partialclone", "origin", true},
		{"0", "worktreeconfig", "true", true},
		{"0", "objectformat", "sha256", false},
		{"0", "refstorage", "reftable", false},
		{"1", "somethingnew", "true", false},
		{"2", "", "", false},
	} {
		r := gittest.Init(t)
		r.Commit("one")
		r.Git("config", "core.repositoryformatversion", tc.version)
		if tc.ext != "" {
			r.Git("config", "extensions."+tc.ext, tc.value)
		}
		_, err := r.Run(nil, "rev-parse", "HEAD")
		if gitWorks := err == nil; gitWorks != tc.works {
			t.Fatalf("v%s %s: git works %v", tc.version, tc.ext, gitWorks)
		}
		c := caps(t, r)
		if c.NativeRefs() != tc.works || c.NativeObjects() != (tc.works && tc.ext != "partialclone") || c.HashSize != 20 {
			t.Errorf("v%s %s: %+v", tc.version, tc.ext, c)
		}
		if tc.ext == "partialclone" && !c.PartialClone {
			t.Errorf("v0 partialclone not honoured: %+v", c)
		}
		if !tc.works && len(c.Fallbacks()) == 0 {
			t.Errorf("v%s %s: no fallback named", tc.version, tc.ext)
		}
	}
}

// Variables that change what git reads of replace refs and grafts, in the
// app's own environment, are not given to git, so they do not change what
// the engine decides either: native history and git's agree.
func TestReplaceVariablesOfTheApp(t *testing.T) {
	r := gittest.Init(t)
	a := r.Commit("a")
	b := r.Commit("b")
	r.Git("replace", a, b)
	grafts := filepath.Join(t.TempDir(), "grafts")
	if err := os.WriteFile(grafts, []byte(a+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_NO_REPLACE_OBJECTS", "1")
	t.Setenv("GIT_REPLACE_REF_BASE", "refs/elsewhere/")
	t.Setenv("GIT_GRAFT_FILE", grafts)
	if c := caps(t, r); !c.ReplaceRefs || c.Grafts {
		t.Fatalf("%+v", c)
	}
}
