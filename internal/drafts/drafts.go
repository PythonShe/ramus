// Package drafts keeps the commit message being written in each
// repository, so that one not yet committed survives the app quitting.
package drafts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"

	"ramus/internal/settings"
)

// maxFile bounds a draft read back: a message is text a person typed.
const maxFile = 4 << 20

// Draft is a commit message not yet committed.
type Draft struct {
	Subject   string `json:"subject,omitempty"`
	Body      string `json:"body,omitempty"`
	CoAuthors string `json:"coAuthors,omitempty"`
}

// Empty reports whether there is nothing to keep.
func (d Draft) Empty() bool { return d == Draft{} }

// file is a draft as written: the repository's path is kept with it, so
// that a file is never read as another repository's.
type file struct {
	Path string `json:"path"`
	Draft
}

// Store keeps drafts, one file per repository, in a folder. Writes run
// off the caller's thread, the latest draft of each repository only, one
// at a time. Safe for use by several goroutines.
type Store struct {
	dir string
	// OnError hears a write that failed, on the writing goroutine.
	OnError func(error)

	mu      sync.Mutex
	pending map[string]Draft
	busy    bool
	// write serializes writes, Keep's and Flush's.
	write sync.Mutex
}

// New keeps drafts in dir, made when the first one is written.
func New(dir string) *Store { return &Store{dir: dir, pending: map[string]Draft{}} }

func (s *Store) name(path string) string {
	sum := sha256.Sum256([]byte(path))
	return filepath.Join(s.dir, hex.EncodeToString(sum[:12])+".json")
}

// Load reads the draft of the repository at path, none when there is
// none. It reads the disk: never call it on the main thread.
func (s *Store) Load(path string) (Draft, error) {
	s.mu.Lock()
	d, ok := s.pending[path]
	s.mu.Unlock()
	if ok {
		return d, nil
	}
	f, err := os.Open(s.name(path))
	if errors.Is(err, os.ErrNotExist) {
		return Draft{}, nil
	}
	if err != nil {
		return Draft{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFile+1))
	if err != nil {
		return Draft{}, err
	}
	var v file
	if len(data) > maxFile || json.Unmarshal(data, &v) != nil {
		return Draft{}, errors.New("drafts: " + f.Name() + " is not a draft")
	}
	if v.Path != path {
		return Draft{}, nil
	}
	return v.Draft, nil
}

// Keep has d written as the draft of the repository at path soon; an
// empty one removes it.
func (s *Store) Keep(path string, d Draft) {
	s.mu.Lock()
	s.pending[path] = d
	if s.busy {
		s.mu.Unlock()
		return
	}
	s.busy = true
	s.mu.Unlock()
	go s.run()
}

func (s *Store) run() {
	for {
		s.write.Lock()
		s.mu.Lock()
		var path string
		var d Draft
		found := false
		for path, d = range s.pending {
			found = true
			delete(s.pending, path)
			break
		}
		if !found {
			s.busy = false
			s.mu.Unlock()
			s.write.Unlock()
			return
		}
		s.mu.Unlock()
		err := s.save(path, d)
		s.write.Unlock()
		if err != nil && s.OnError != nil {
			s.OnError(err)
		}
	}
}

// Flush writes every draft waiting now, for the app's quit, which cannot
// wait for a goroutine.
func (s *Store) Flush() error {
	s.write.Lock()
	defer s.write.Unlock()
	s.mu.Lock()
	pending := s.pending
	s.pending = map[string]Draft{}
	s.mu.Unlock()
	var errs []error
	for path, d := range pending {
		errs = append(errs, s.save(path, d))
	}
	return errors.Join(errs...)
}

func (s *Store) save(path string, d Draft) error {
	name := s.name(path)
	if d.Empty() {
		if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	data, err := json.Marshal(file{path, d})
	if err != nil {
		return err
	}
	return settings.WriteFile(name, append(data, '\n'))
}
