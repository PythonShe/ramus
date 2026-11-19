package run

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// removeLocks removes the lock files a git stopped without notice left
// in the repository of gitDir: the *.lock files at the top of its git
// directory and of the common one (index, HEAD, packed-refs, config) and
// under refs/, made since it started. Its writes are the app's only ones
// in the repository (the write queue runs one at a time, and reads take
// no locks), so a lock made since then is its own, but for one a git in
// a terminal took in the moment since.
func removeLocks(gitDir string, since time.Time) {
	dirs := []string{gitDir}
	if b, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		common := strings.TrimSpace(string(b))
		if !filepath.IsAbs(common) {
			common = filepath.Join(gitDir, common)
		}
		dirs = append(dirs, filepath.Clean(common))
	}
	fresh := func(path string, d fs.DirEntry) bool {
		if !d.Type().IsRegular() || !strings.HasSuffix(d.Name(), ".lock") {
			return false
		}
		info, err := d.Info()
		return err == nil && !info.ModTime().Before(since)
	}
	for _, dir := range dirs {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if p := filepath.Join(dir, e.Name()); fresh(p, e) {
				os.Remove(p)
			}
		}
		filepath.WalkDir(filepath.Join(dir, "refs"), func(p string, d fs.DirEntry, err error) error {
			if err == nil && fresh(p, d) {
				os.Remove(p)
			}
			return nil
		})
	}
}
