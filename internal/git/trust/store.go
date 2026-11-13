package trust

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

// Store remembers which repositories the user trusts, in a file of the
// app's own (never in a repository, where the repository could claim to
// be trusted). A repository is named by its common git directory, so all
// its worktrees share one answer. Safe for use by several goroutines; the
// file is written outside the lock.
type Store struct {
	path string
	mu   sync.Mutex
	dirs []string // sorted
}

type file struct {
	Trusted []string `json:"trusted"`
}

// Load reads the store at path; a missing file is an empty store.
func Load(path string) (*Store, error) {
	s := &Store{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	for _, d := range f.Trusted {
		s.dirs = append(s.dirs, clean(d))
	}
	slices.Sort(s.dirs)
	s.dirs = slices.Compact(s.dirs)
	return s, nil
}

func clean(dir string) string {
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	return filepath.Clean(dir)
}

// Trusted reports whether the repository with this common git directory
// is trusted.
func (s *Store) Trusted(commonDir string) bool {
	d := clean(commonDir)
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := slices.BinarySearch(s.dirs, d)
	return ok
}

// Set records the user's answer and saves the store.
func (s *Store) Set(commonDir string, trusted bool) error {
	d := clean(commonDir)
	s.mu.Lock()
	i, ok := slices.BinarySearch(s.dirs, d)
	switch {
	case trusted && !ok:
		s.dirs = slices.Insert(s.dirs, i, d)
	case !trusted && ok:
		s.dirs = slices.Delete(s.dirs, i, i+1)
	}
	data, err := json.MarshalIndent(file{Trusted: slices.Clone(s.dirs)}, "", "\t")
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return writeAtomic(s.path, append(data, '\n'))
}

// writeAtomic replaces path whole, so a crash leaves the old file or the
// new one, never half of one.
func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".trust-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
