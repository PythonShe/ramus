package repo

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"ramus/internal/git/run"
)

// Worktree is one work tree of a repository, as git worktree list says.
type Worktree struct {
	Path     string
	Head     string // hex, empty when unborn
	Branch   string // "refs/heads/x"; empty when detached
	Detached bool
	Bare     bool
	// Locked and Prunable are set, with their reason (which may be empty),
	// when git says so.
	Locked, Prunable        bool
	LockReason, PruneReason string
	// Current is the work tree this repository was opened in.
	Current bool
}

// Worktrees lists the repository's work trees, the main one first.
func (r *Repository) Worktrees(ctx context.Context) ([]Worktree, error) {
	res, err := r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, Read: true, Args: []string{"worktree", "list", "--porcelain", "-z"}})
	if err != nil {
		return nil, err
	}
	wts := parseWorktrees(res.Stdout)
	here := r.WorkTree
	if real, err := filepath.EvalSymlinks(here); err == nil {
		here = real
	}
	for i := range wts {
		p := wts[i].Path
		if real, err := filepath.EvalSymlinks(p); err == nil {
			p = real
		}
		wts[i].Current = here != "" && p == here
	}
	return wts, nil
}

func parseWorktrees(out []byte) []Worktree {
	var wts []Worktree
	var cur *Worktree
	for _, f := range bytes.Split(out, []byte{0}) {
		if len(f) == 0 {
			cur = nil // a record ends with an empty field
			continue
		}
		key, val, _ := strings.Cut(string(f), " ")
		if key == "worktree" {
			wts = append(wts, Worktree{Path: val})
			cur = &wts[len(wts)-1]
			continue
		}
		if cur == nil {
			continue
		}
		switch key {
		case "HEAD":
			if strings.Trim(val, "0") != "" {
				cur.Head = val
			}
		case "branch":
			cur.Branch = val
		case "detached":
			cur.Detached = true
		case "bare":
			cur.Bare = true
		case "locked":
			cur.Locked, cur.LockReason = true, val
		case "prunable":
			cur.Prunable, cur.PruneReason = true, val
		}
	}
	return wts
}

// Submodule is a submodule as .gitmodules declares it.
type Submodule struct {
	Name, Path, URL string
}

// Submodules lists the submodules .gitmodules in the work tree declares.
// The file is read as data through git config --file, which runs nothing
// from it.
func (r *Repository) Submodules(ctx context.Context) ([]Submodule, error) {
	if r.WorkTree == "" {
		return nil, nil
	}
	file := filepath.Join(r.WorkTree, ".gitmodules")
	if _, err := os.Lstat(file); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	res, err := r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, Read: true,
		Args: []string{"config", "--file", file, "-z", "--get-regexp", `^submodule\..*\.(path|url)$`}})
	var e *run.Error
	if errors.As(err, &e) && e.ExitCode == 1 {
		return nil, nil // no submodule entries
	}
	if err != nil {
		return nil, err
	}
	return parseSubmodules(res.Stdout), nil
}

func parseSubmodules(out []byte) []Submodule {
	byName := map[string]*Submodule{}
	var order []string
	for _, rec := range bytes.Split(out, []byte{0}) {
		key, val, ok := strings.Cut(string(rec), "\n")
		if !ok {
			continue
		}
		rest, ok := strings.CutPrefix(key, "submodule.")
		if !ok {
			continue
		}
		dot := strings.LastIndexByte(rest, '.')
		if dot < 0 {
			continue
		}
		name, field := rest[:dot], rest[dot+1:]
		s := byName[name]
		if s == nil {
			s = &Submodule{Name: name}
			byName[name] = s
			order = append(order, name)
		}
		switch field {
		case "path":
			s.Path = val
		case "url":
			s.URL = val
		}
	}
	var out2 []Submodule
	for _, n := range order {
		if s := byName[n]; s.Path != "" {
			out2 = append(out2, *s)
		}
	}
	slices.SortFunc(out2, func(a, b Submodule) int { return strings.Compare(a.Path, b.Path) })
	return out2
}
