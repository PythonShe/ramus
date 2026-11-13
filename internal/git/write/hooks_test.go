package write

import (
	"context"
	"errors"
	"slices"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

func TestHooks(t *testing.T) {
	r := gittest.Init(t)
	rn := testrun.Runner(t)
	hs, err := Hooks(context.Background(), rn, r.GitDir(), r.Dir, "pre-commit")
	if errors.Is(err, ErrNoHookList) {
		t.Skip(err)
	}
	if err != nil || len(hs) != 0 {
		t.Fatalf("no hooks: %v %+v", hs, err)
	}
	hook(t, r, "pre-commit", "true")
	r.Git("config", "hook.lint.command", "echo lint")
	r.Git("config", "--add", "hook.lint.event", "pre-commit")
	hs, err = Hooks(context.Background(), rn, r.GitDir(), r.Dir, "pre-commit")
	want := []Hook{{Name: "lint", Scope: "local"}, {Name: "hook from hookdir"}}
	if err != nil || !slices.Equal(hs, want) {
		t.Fatalf("hooks %+v %v, want %+v", hs, err, want)
	}
}
