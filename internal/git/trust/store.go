package trust

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"ramus/internal/git/pathsafe"
	"ramus/internal/statefile"
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
	// What a later version of the app wrote, kept for it as it is: the
	// keys of the file (extra) and of each trusted folder's entry (kept)
	// this version does not know, and the entries of folders not
	// trusted, by the path the file names them by (others).
	extra  map[string]json.RawMessage
	kept   map[string]map[string]json.RawMessage
	others map[string]json.RawMessage
	// broken is why the file could not be read and was left in place:
	// Set then refuses, rather than write over the user's list.
	broken error
	// write keeps saves in order, each of a list no older than the one
	// saved before it.
	write sync.Mutex
	// saving counts the saves under way; idle is closed when the count
	// falls to none.
	saving int
	idle   chan struct{}
}

// writeHook, when set, runs as a save begins to write its file:
// a test holds a save there.
var writeHook func()

// file is the store as trust.json holds it. trusted stays a list of
// paths, which is all the versions before 1.0 read; what is known of a
// folder besides is an object in folders, by path, where a later
// version adds keys of its own.
type file struct {
	Trusted []string                   `json:"trusted"`
	Folders map[string]json.RawMessage `json:"folders,omitempty"`
}

// folder is what this version knows of a trusted folder.
type folder struct {
	// ID is the folder's identity when it was trusted.
	ID string `json:"id,omitempty"`
}

// Load reads the store at path; a missing file is an empty store. A file
// that cannot be read or parsed is moved aside, as the app's other state
// files are, and the error is a *statefile.LoadError saying where; the
// store returned is empty and usable: every repository is asked about
// again, which is the safe side. When it cannot be moved, the store
// refuses to save over it.
func Load(path string) (*Store, error) {
	s := &Store{path: path, ids: map[string]string{}, kept: map[string]map[string]json.RawMessage{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	var f file
	if err == nil {
		err = json.Unmarshal(data, &f)
	}
	if err != nil {
		e := statefile.Unreadable(path, err)
		if e.KeepErr != nil {
			s.broken = e
		}
		return s, e
	}
	s.extra = unknown(data, "trusted", "folders")
	for _, d := range f.Trusted {
		c := clean(d)
		s.dirs = append(s.dirs, c)
		raw, ok := f.Folders[d]
		if !ok {
			continue
		}
		delete(f.Folders, d)
		var fo folder
		// An entry that does not read gives no identity: the folder
		// there first does, as for a list from before identities.
		if json.Unmarshal(raw, &fo) == nil && fo.ID != "" {
			s.ids[c] = fo.ID
		}
		if x := unknown(raw, "id"); x != nil {
			s.kept[c] = x
		}
	}
	if len(f.Folders) > 0 {
		s.others = f.Folders
	}
	slices.Sort(s.dirs)
	s.dirs = slices.Compact(s.dirs)
	return s, nil
}

// unknown is the keys of the JSON object obj but those named, nil when
// there are none or obj is no object.
func unknown(obj []byte, known ...string) map[string]json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(obj, &m) != nil {
		return nil
	}
	for _, k := range known {
		delete(m, k)
	}
	if len(m) == 0 {
		return nil
	}
	return m
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
		adopted := s.ids[d] == ""
		if adopted {
			s.ids[d] = got
		}
		want = s.ids[d]
		s.mu.Unlock()
		// Kept in memory only, the identity would be that of whatever
		// folder is at the path at the next launch.
		if adopted {
			s.adopt()
		}
	}
	return got == want
}

// adopt saves the identities an entry trusted before they were kept
// took from its folder. One that is not saved is taken again, from
// the folder there then, which is the trust of before.
func (s *Store) adopt() {
	defer s.begin()()
	s.write.Lock()
	defer s.write.Unlock()
	s.mu.Lock()
	if s.broken != nil {
		s.mu.Unlock()
		return
	}
	data, err := s.marshal()
	s.mu.Unlock()
	if err == nil {
		writeAtomic(s.path, append(data, '\n'))
	}
}

// Saving counts a save the caller is about to make, from before the
// goroutine that makes it starts, until the func it returns is called:
// a quit right after waits for it (Wait).
func (s *Store) Saving() func() { return s.begin() }

// begin counts a save under way, for Wait, until the func it returns is
// called.
func (s *Store) begin() func() {
	s.mu.Lock()
	if s.saving == 0 {
		s.idle = make(chan struct{})
	}
	s.saving++
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		if s.saving--; s.saving == 0 {
			close(s.idle)
		}
		s.mu.Unlock()
	}
}

// Set records the user's answer and saves the store. A folder whose
// identity cannot be read, gone since it was asked about, is not
// trusted: Set says why.
func (s *Store) Set(commonDir string, trusted bool) error {
	defer s.begin()()
	d := clean(commonDir)
	id := ""
	if trusted {
		// Saved by its path alone, the answer would hold for whatever
		// folder is there later.
		var err error
		if id, err = identity(d); err != nil {
			return fmt.Errorf("trust: %s cannot be trusted, as it cannot be told apart from another put in its place: %w", d, err)
		}
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
		if raw, ok := s.others[d]; ok {
			if x := unknown(raw, "id"); x != nil {
				s.kept[d] = x
			}
			delete(s.others, d)
		}
	case !trusted && ok:
		s.dirs = slices.Delete(s.dirs, i, i+1)
	}
	if trusted {
		s.ids[d] = id
	} else {
		// What a later version knew of the folder went with the answer.
		delete(s.ids, d)
		delete(s.kept, d)
		delete(s.others, d)
	}
	data, err := s.marshal()
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return writeAtomic(s.path, append(data, '\n'))
}

// Wait waits, up to d, for the answers being saved to be in the file,
// at a quit, after which the process may end at once; d bounds it for a
// file on a volume that does not answer. It reports whether they were.
func (s *Store) Wait(d time.Duration) bool {
	s.mu.Lock()
	idle := s.idle
	if s.saving == 0 {
		idle = nil
	}
	s.mu.Unlock()
	if idle == nil {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-idle:
		return true
	case <-t.C:
		return false
	}
}

// marshal is the store as its file holds it, with what a later version
// wrote. Call it with s.mu held.
func (s *Store) marshal() ([]byte, error) {
	out := map[string]any{}
	for k, v := range s.extra {
		out[k] = v
	}
	folders := map[string]json.RawMessage{}
	for k, v := range s.others {
		folders[k] = v
	}
	for _, dir := range s.dirs {
		entry := map[string]any{}
		for k, v := range s.kept[dir] {
			entry[k] = v
		}
		if id := s.ids[dir]; id != "" {
			entry["id"] = id
		}
		if len(entry) == 0 {
			continue
		}
		raw, err := json.Marshal(entry)
		if err != nil {
			return nil, err
		}
		folders[dir] = raw
	}
	out["trusted"] = slices.Clone(s.dirs)
	if len(folders) > 0 {
		out["folders"] = folders
	}
	return json.MarshalIndent(out, "", "\t")
}

// writeAtomic replaces path whole, as the app's other state files are
// written: a crash leaves the old file or the new one, never half of one,
// and on Windows a file another program holds open is tried again.
func writeAtomic(path string, data []byte) error {
	if writeHook != nil {
		writeHook()
	}
	return statefile.WriteFile(path, data)
}
