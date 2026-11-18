package repo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/run"
)

// porcelain is git status as the repository's runner gives it.
func porcelain(t *testing.T, r *Repository) string {
	t.Helper()
	res, err := r.Runner.Run(context.Background(), &run.Cmd{GitDir: r.GitDir, WorkTree: r.WorkTree, Read: true,
		Args: []string{"status", "--porcelain", "--untracked-files=normal"}})
	if err != nil {
		t.Fatal(err)
	}
	return string(res.Stdout)
}

// Dependency folders are left out as git leaves out what info/exclude
// ignores, the user's own rules kept, but for a name the repository
// tracks a file under; staging a folder that holds one leaves it out.
func TestHideFolders(t *testing.T) {
	g := gittest.Init(t)
	own := filepath.Join(t.TempDir(), "ignore")
	if err := os.WriteFile(own, []byte("*.log"), 0o644); err != nil {
		t.Fatal(err)
	}
	g.Git("config", "core.excludesFile", own)
	g.Write("lib/.venv/kept.txt", "kept on purpose")
	g.Commit("one")
	for _, f := range []string{"node_modules/a/index.js", "packages/new/a.js", "packages/new/node_modules/b.js",
		"src/__pycache__/m.pyc", "lib/.venv/new.txt", ".venv/pyvenv.cfg", "app.log", "x.tmp"} {
		g.Write(f, "x")
	}
	r, err := Open(context.Background(), testrun.Runner(t), g.Dir, trustAll)
	if err != nil {
		t.Fatal(err)
	}
	shown := porcelain(t, r)
	if changed, err := r.HideFolders(DependencyFolders); !changed || err != nil {
		t.Fatalf("changed %v, %v", changed, err)
	}
	if changed, err := r.HideFolders(DependencyFolders); changed || err != nil {
		t.Fatalf("again: changed %v, %v", changed, err)
	}
	got := porcelain(t, r)
	// The oracle: git with the same rules in info/exclude. .venv is
	// tracked under lib/, so it stays.
	g.Write(".git/info/exclude", "node_modules/\n__pycache__/\n")
	if want := g.Git("status", "--porcelain", "--untracked-files=normal"); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	os.Remove(filepath.Join(g.GitDir(), "info", "exclude"))
	for _, s := range []string{"?? packages/", "?? .venv/", "?? lib/.venv/new.txt", "?? x.tmp"} {
		if !strings.Contains(got, s+"\n") {
			t.Fatalf("no %q in\n%s", s, got)
		}
	}
	for _, s := range []string{"node_modules", "__pycache__", "app.log"} {
		if strings.Contains(got, s) {
			t.Fatalf("%q in\n%s", s, got)
		}
	}
	files, err := r.UntrackedUnder(context.Background(), "packages/", 10)
	if err != nil || strings.Join(files.Paths, " ") != "packages/new/a.js" {
		t.Fatalf("under packages/: %q, %v", files.Paths, err)
	}
	if _, err := r.Runner.Run(context.Background(), &run.Cmd{GitDir: r.GitDir, WorkTree: r.WorkTree,
		Args: []string{"add", "--all", "--", "packages/"}}); err != nil {
		t.Fatal(err)
	}
	if staged := g.Git("ls-files", "packages"); staged != "packages/new/a.js\n" {
		t.Fatalf("staged %q", staged)
	}

	// The user's own file changes: its new rule holds.
	if err := os.WriteFile(own, []byte("*.log\n*.tmp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed, _ := r.HideFolders(DependencyFolders); !changed || strings.Contains(porcelain(t, r), "x.tmp") {
		t.Fatalf("changed %v:\n%s", changed, porcelain(t, r))
	}
	// Tracking a file under a name shows it.
	g.Git("add", "-f", "src/__pycache__/m.pyc")
	if changed, _ := r.HideFolders(DependencyFolders); !changed {
		t.Fatal("tracking __pycache__ changed nothing")
	}

	// None puts the user's rules back alone.
	if changed, _ := r.HideFolders(nil); !changed {
		t.Fatal("none changed nothing")
	}
	if got := porcelain(t, r); !strings.Contains(got, "?? node_modules/") || strings.Contains(got, "app.log") {
		t.Fatalf("none hidden:\n%s\nwas\n%s", got, shown)
	}
	file := r.hidden.file
	if _, err := os.Stat(file); err != nil {
		t.Fatal(err)
	}
	r.Close()
	<-r.Life().Done()
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("the excludes file outlives the repository: %v", err)
	}
}

// With no core.excludesFile the user's rules are git's default file's,
// under XDG_CONFIG_HOME.
func TestHideFoldersKeepsDefaultExcludes(t *testing.T) {
	g := gittest.Init(t)
	g.Commit("one")
	xdg := t.TempDir()
	os.MkdirAll(filepath.Join(xdg, "git"), 0o755)
	os.WriteFile(filepath.Join(xdg, "git", "ignore"), []byte("*.log\n"), 0o644)
	g.Write("app.log", "x")
	g.Write("node_modules/a.js", "x")
	base := testrun.Runner(t)
	runner := run.New(base.Git(), append(base.Environ(), "XDG_CONFIG_HOME="+xdg), 4)
	r, err := Open(context.Background(), runner, g.Dir, trustAll)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := r.HideFolders(DependencyFolders); err != nil {
		t.Fatal(err)
	}
	if got := porcelain(t, r); got != "" {
		t.Fatalf("status %q", got)
	}
}

// The config read again, which holds the excludes file HideFolders
// gives git, changes nothing: the user's own file is still the one
// copied.
func TestHideFoldersAfterConfigRead(t *testing.T) {
	g := gittest.Init(t)
	own := filepath.Join(t.TempDir(), "ignore")
	os.WriteFile(own, []byte("*.log\n"), 0o644)
	g.Git("config", "core.excludesFile", own)
	g.Commit("one")
	g.Write("node_modules/a.js", "x")
	r, err := Open(context.Background(), testrun.Runner(t), g.Dir, trustAll)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if changed, err := r.HideFolders(DependencyFolders); !changed || err != nil {
		t.Fatalf("changed %v, %v", changed, err)
	}
	first := r.hidden.content
	for range 3 {
		if err := r.ReloadConfig(context.Background()); err != nil {
			t.Fatal(err)
		}
		if changed, err := r.HideFolders(DependencyFolders); changed || err != nil || r.hidden.content != first {
			t.Fatalf("changed %v, %v:\n%s", changed, err, r.hidden.content)
		}
	}
}

// A name the user's own rules take back stays; an excludes file whose
// path the engine does not resolve as git does hides nothing rather
// than drop the user's rules.
func TestHideFoldersRespectsTheUser(t *testing.T) {
	g := gittest.Init(t)
	own := filepath.Join(t.TempDir(), "ignore")
	os.WriteFile(own, []byte("!node_modules/\n"), 0o644)
	g.Git("config", "core.excludesFile", own)
	g.Commit("one")
	g.Write("node_modules/a.js", "x")
	g.Write(".venv/b", "x")
	r, err := Open(context.Background(), testrun.Runner(t), g.Dir, trustAll)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.HideFolders(DependencyFolders)
	if got := porcelain(t, r); got != "?? node_modules/\n" {
		t.Fatalf("status %q", got)
	}
	g.Git("config", "core.excludesFile", "~nobody/ignore")
	if err := r.ReloadConfig(context.Background()); err != nil {
		t.Fatal(err)
	}
	if changed, _ := r.HideFolders(DependencyFolders); !changed || r.hidden.content != "" {
		t.Fatalf("changed %v, content %q", changed, r.hidden.content)
	}
	if got := porcelain(t, r); !strings.Contains(got, "?? .venv/") {
		t.Fatalf("status %q", got)
	}
}
