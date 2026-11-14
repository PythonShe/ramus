package repo

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"ramus/internal/git/run"
)

// OwnershipError is git's "dubious ownership": the repository belongs to
// another user, and git will not work in it until the user says it is
// safe. The app explains it and shows Fix; it never runs it.
type OwnershipError struct {
	Path string
	// Fix is the command git suggests, to copy into a terminal.
	Fix string
}

func (e *OwnershipError) Error() string {
	return fmt.Sprintf("repo: %s belongs to another user; git refuses to work in it until it is marked safe", e.Path)
}

// CheckOwnership applies git's ownership check, safe.directory included,
// before the engine's own readers touch the repository: git runs a
// discovery there, since naming the git directory would skip the check.
func CheckOwnership(ctx context.Context, r *run.Runner, loc Location) error {
	dir := loc.WorkTree
	if dir == "" {
		dir = loc.GitDir
	}
	_, err := r.Run(ctx, &run.Cmd{
		GitDir: loc.GitDir, WorkTree: dir, Discover: true, Read: true, Untranslated: true,
		Args: []string{"rev-parse", "--git-dir"},
		// A bare repository opened on purpose is explicit for this check.
		Config: []string{"safe.bareRepository=all"},
	})
	var e *run.Error
	if err == nil || !errors.As(err, &e) {
		return err
	}
	const marker = "detected dubious ownership in repository at '"
	i := strings.Index(e.Stderr, marker)
	if i < 0 {
		return err
	}
	path, _, _ := strings.Cut(e.Stderr[i+len(marker):], "'\n")
	fix := "git config --global --add safe.directory " + path
	for _, line := range strings.Split(e.Stderr, "\n") {
		if l := strings.TrimSpace(line); strings.HasPrefix(l, "git config --global --add safe.directory") {
			fix = l
		}
	}
	// git names the folder with forward slashes on Windows too; its hint
	// is kept as git wrote it, which git takes back.
	return &OwnershipError{Path: filepath.FromSlash(path), Fix: fix}
}

// ErrEmbeddedBare is a bare repository inside another repository's work
// tree: a cloned project can carry one to run its config on whoever opens
// it, which is why git's safe.bareRepository exists.
var ErrEmbeddedBare = errors.New("repo: a bare repository inside another repository's work tree is not opened")

// checkEmbeddedBare refuses a bare repository with a work tree above it.
func checkEmbeddedBare(loc Location) error {
	if !loc.Bare {
		return nil
	}
	outer, err := Discover(filepath.Dir(loc.GitDir))
	if err != nil || outer.WorkTree == "" {
		return nil
	}
	if rel, err := filepath.Rel(outer.WorkTree, loc.GitDir); err == nil && !strings.HasPrefix(rel, "..") {
		return fmt.Errorf("%w: %s is inside %s", ErrEmbeddedBare, loc.GitDir, outer.WorkTree)
	}
	return nil
}
