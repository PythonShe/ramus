package trust

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"

	"ramus/internal/git/pathsafe"
)

// Store remembers which repositories the user trusts, in a file of the
// app's own (never in a repository, where the repository could claim to
// be trusted). A repository is named by its common git directory, so all
// its worktrees share one answer, and the answer holds for the folder
// that was there when it was given: another put at the same path later
// (an archive extracted again) is asked about anew. Safe for use by
// several goroutines; the file is written outside the lock.
type Store struct {
	path string
	mu   sync.Mutex
	dirs []string // sorted
	// ids is the identity of each trusted folder when it was trusted;
	// none, or "", for one trusted before identities were kept, which the
	// folder found there first gives.
	ids map[string]string
	// broken is why the file could not be read and was left in place:
	// Set then refuses, rather than write over the user's list.
	broken error
	// write keeps saves in order, each of a list no older than the one
	// saved before it.
	write sync.Mutex
}

type file struct {
	Trusted []string `json:"trusted"`
	// IDs are the identities of the trusted folders, by path.
	IDs map[string]string `json:"ids,omitempty"`
}

// Load reads the store at path; a missing file is an empty store. A file
// that cannot be read or parsed is moved aside, to the name the error
// gives, and the store returned is empty and usable: every repository is
// asked about again, which is the safe side. When it cannot be moved, the
// store refuses to save over it.
func Load(path string) (*Store, error) {
	s := &Store{path: path, ids: map[string]string{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	var f file
	if err == nil {
		err = json.Unmarshal(data, &f)
	}
	if err != nil {
		bad, merr := moveAside(path)
		if merr != nil {
			s.broken = fmt.Errorf("trust: %s cannot be read (%v) nor moved aside (%v); no trust given is saved until it is", path, err, merr)
			return s, s.broken
		}
		return s, fmt.Errorf("trust: %s cannot be read (%v); it was moved to %s, and every repository asks to be trusted again", path, err, bad)
	}
	for _, d := range f.Trusted {
		c := clean(d)
		s.dirs = append(s.dirs, c)
		if id, ok := f.IDs[d]; ok {
			s.ids[c] = id
		}
	}
	slices.Sort(s.dirs)
	s.dirs = slices.Compact(s.dirs)
	return s, nil
}

// moveAside renames the file at path to a name of its own beside it.
func moveAside(path string) (string, error) {
	for i := 1; i <= 100; i++ {
		bad := path + ".bad"
		if i > 1 {
			bad += strconv.Itoa(i)
		}
		if _, err := os.Lstat(bad); errors.Is(err, fs.ErrNotExist) {
			return bad, os.Rename(path, bad)
		}
	}
	return "", errors.New("too many files moved aside already")
}

func clean(dir string) string {
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	if real, err := pathsafe.Real(dir); err == nil {
		dir = real
	}
	return filepath.Clean(dir)
}

// Trusted reports whether the repository with this common git directory
// is trusted: the user said so of the folder there now.
func (s *Store) Trusted(commonDir string) bool {
	d := clean(commonDir)
	s.mu.Lock()
	_, ok := slices.BinarySearch(s.dirs, d)
	want := s.ids[d]
	s.mu.Unlock()
	if !ok {
		return false
	}
	got, err := identity(d)
	if err != nil {
		return false
	}
	if want == "" {
		s.mu.Lock()
		if s.ids[d] == "" {
			s.ids[d] = got
		}
		want = s.ids[d]
		s.mu.Unlock()
	}
	return got == want
}

// Set records the user's answer and saves the store.
func (s *Store) Set(commonDir string, trusted bool) error {
	d := clean(commonDir)
	id := ""
	if trusted {
		id, _ = identity(d)
	}
	s.write.Lock()
	defer s.write.Unlock()
	s.mu.Lock()
	if s.broken != nil {
		s.mu.Unlock()
		return s.broken
	}
	i, ok := slices.BinarySearch(s.dirs, d)
	switch {
	case trusted && !ok:
		s.dirs = slices.Insert(s.dirs, i, d)
	case !trusted && ok:
		s.dirs = slices.Delete(s.dirs, i, i+1)
	}
	if trusted {
		s.ids[d] = id
	} else {
		delete(s.ids, d)
	}
	f := file{Trusted: slices.Clone(s.dirs), IDs: map[string]string{}}
	for _, dir := range s.dirs {
		if id := s.ids[dir]; id != "" {
			f.IDs[dir] = id
		}
	}
	data, err := json.MarshalIndent(f, "", "\t")
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
