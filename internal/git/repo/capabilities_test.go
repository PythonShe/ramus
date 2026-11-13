package repo

import (
	"context"
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

	unknown := gittest.Init(t)
	unknown.Git("config", "core.repositoryformatversion", "1")
	unknown.Git("config", "extensions.somethingNew", "true")
	if c := caps(t, unknown); len(c.Unknown) != 1 || c.NativeRefs() {
		t.Fatalf("unknown extension: %+v", c)
	}
}
