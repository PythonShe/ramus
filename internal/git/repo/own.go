package repo

import (
	"os"
	"path/filepath"
	"strings"

	"ramus/internal/git/config"
	"ramus/internal/git/pathsafe"
)

// TrustDir is the directory the user's trust in the repository at loc,
// whose config is cfg, is recorded under. It is the common git directory
// when git itself made the work tree and the git directory one
// repository: the folder holding its .git directory, a worktree that git
// worktree add registered (its git directory under the common one's
// worktrees/ names this work tree back), a work tree its git directory's
// core.worktree names (a submodule's). Any other work tree is its own
// answer: a .git file or a commondir can name any repository on disk,
// and a folder from elsewhere that names a trusted one would otherwise
// open as it, with the trusted config's hooks taken from the folder.
func TrustDir(loc Location, cfg *config.Config) string {
	if owned(loc, cfg) {
		return loc.CommonDir
	}
	if loc.WorkTree != "" {
		return loc.WorkTree
	}
	return loc.GitDir
}

func owned(loc Location, cfg *config.Config) bool {
	if loc.GitDir != loc.CommonDir {
		// Linked: its git directory is one of the common directory's
		// worktrees, which nothing but git (or the owner) makes.
		if filepath.Dir(loc.GitDir) != filepath.Join(loc.CommonDir, "worktrees") {
			return false
		}
		if loc.WorkTree == "" {
			return true
		}
		// And it names this work tree, as git worktree add wrote it, and
		// git worktree repair writes it again after a move.
		b, err := pathsafe.ReadFile(filepath.Join(loc.GitDir, "gitdir"), smallFile)
		if err != nil {
			return false
		}
		back := strings.TrimSpace(string(b))
		if back == "" {
			return false
		}
		if !filepath.IsAbs(back) {
			back = filepath.Join(loc.GitDir, back)
		}
		return resolve(back) == resolve(filepath.Join(loc.WorkTree, ".git"))
	}
	if loc.WorkTree == "" {
		return true // bare
	}
	dotgit := filepath.Join(loc.WorkTree, ".git")
	if st, err := os.Lstat(dotgit); err == nil && st.IsDir() && resolve(dotgit) == loc.GitDir {
		return true
	}
	if cfg == nil {
		return false
	}
	// A git directory apart from its work tree, as a submodule's in its
	// superproject's modules/: its config names the work tree, which git
	// then takes, whatever .git file led to it.
	wt, ok := cfg.Get("core.worktree")
	if !ok || wt == "" {
		return false
	}
	if !filepath.IsAbs(wt) {
		wt = filepath.Join(loc.GitDir, wt)
	}
	return resolve(wt) == loc.WorkTree
}
