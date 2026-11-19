package run

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Where git cannot remove its lock files as it is stopped (Windows), the
// runner removes those a write it stopped left; elsewhere git removes its
// own on SIGTERM, and only one stopped in the instant between making a
// lock and noting it for removal leaves one, which the next write reports
// and the user can remove (write.LockedError). On every system, a lock
// another write holds is left alone: the app's writes to one worktree run
// one at a time, but another worktree of the same repository, open in
// another tab, writes to the same common directory and refs at once, and
// a git in a terminal may too. That last case, on Windows, within the
// time a stopped write ran, is the one left.

// writes counts the writes Run is running in each repository, by common
// git directory, for removeLocks to tell whether the repository's shared
// locks may be another's.
type writes struct {
	mu sync.Mutex
	n  map[string]int
}

// add counts a write in the repository whose common directory is common.
func (w *writes) add(common string, d int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.n == nil {
		w.n = map[string]int{}
	}
	w.n[common] += d
	if w.n[common] <= 0 {
		delete(w.n, common)
	}
}

// others reports whether a write besides one counted runs in common.
func (w *writes) others(common string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.n[common] > 1
}

// commonDir is the common git directory of gitDir: gitDir itself unless
// it is a linked worktree's.
func commonDir(gitDir string) string {
	if b, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		common := strings.TrimSpace(string(b))
		if !filepath.IsAbs(common) {
			common = filepath.Join(gitDir, common)
		}
		return filepath.Clean(common)
	}
	return filepath.Clean(gitDir)
}

// removeLocks removes the lock files a git stopped without notice left
// in the repository of gitDir, made since it started: the *.lock files at
// the top of its git directory and of the common one (index, HEAD,
// packed-refs, config) and under refs/. When shared, another write of the
// app runs in the same repository, and only the locks no other worktree
// takes go: this worktree's index.lock and HEAD.lock.
func removeLocks(gitDir string, since time.Time, shared bool) {
	fresh := func(d fs.DirEntry) bool {
		if !d.Type().IsRegular() || !strings.HasSuffix(d.Name(), ".lock") {
			return false
		}
		info, err := d.Info()
		return err == nil && !info.ModTime().Before(since)
	}
	if shared {
		entries, _ := os.ReadDir(gitDir)
		for _, e := range entries {
			if (e.Name() == "index.lock" || e.Name() == "HEAD.lock") && fresh(e) {
				os.Remove(filepath.Join(gitDir, e.Name()))
			}
		}
		return
	}
	dirs := []string{gitDir}
	if common := commonDir(gitDir); common != filepath.Clean(gitDir) {
		dirs = append(dirs, common)
	}
	for _, dir := range dirs {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if p := filepath.Join(dir, e.Name()); fresh(e) {
				os.Remove(p)
			}
		}
		filepath.WalkDir(filepath.Join(dir, "refs"), func(p string, d fs.DirEntry, err error) error {
			if err == nil && fresh(d) {
				os.Remove(p)
			}
			return nil
		})
	}
}
