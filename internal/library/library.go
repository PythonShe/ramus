// Package library remembers the repositories the user has opened: the
// recent list, favourites, named groups, and search over all of them.
package library

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"ramus/internal/settings"
)

// Repo is a known repository.
type Repo struct {
	// Path is the work tree (or the git directory of a bare repository),
	// resolved, as the key.
	Path      string    `json:"path"`
	Name      string    `json:"name"`
	Opened    time.Time `json:"opened"`
	Favourite bool      `json:"favourite,omitempty"`
}

// Group is a named set of repositories, opened together.
type Group struct {
	Name  string   `json:"name"`
	Paths []string `json:"paths"`
}

// Library is the store, saved as one file. Safe for use by several
// goroutines.
type Library struct {
	path   string
	mu     sync.Mutex
	repos  []Repo
	groups []Group
}

type file struct {
	Repos  []Repo  `json:"repos"`
	Groups []Group `json:"groups"`
}

// Load reads the library at path; a missing file is an empty library.
func Load(path string) (*Library, error) {
	l := &Library{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return l, err
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return l, err
	}
	l.repos, l.groups = f.Repos, f.Groups
	return l, nil
}

// Clean resolves a path the way the library keys it.
func Clean(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		p = real
	}
	return filepath.Clean(p)
}

func (l *Library) find(p string) int {
	return slices.IndexFunc(l.repos, func(r Repo) bool { return r.Path == p })
}

// Opened records that the repository at path was opened now.
func (l *Library) Opened(path string, at time.Time) error {
	path = Clean(path)
	l.mu.Lock()
	if i := l.find(path); i >= 0 {
		l.repos[i].Opened = at
	} else {
		l.repos = append(l.repos, Repo{Path: path, Name: filepath.Base(path), Opened: at})
	}
	l.mu.Unlock()
	return l.save()
}

// Forget removes a repository from the library and its groups.
func (l *Library) Forget(path string) error {
	path = Clean(path)
	l.mu.Lock()
	if i := l.find(path); i >= 0 {
		l.repos = slices.Delete(l.repos, i, i+1)
	}
	for i := range l.groups {
		l.groups[i].Paths = slices.DeleteFunc(l.groups[i].Paths, func(p string) bool { return p == path })
	}
	l.mu.Unlock()
	return l.save()
}

// SetFavourite marks or unmarks a known repository.
func (l *Library) SetFavourite(path string, on bool) error {
	path = Clean(path)
	l.mu.Lock()
	if i := l.find(path); i >= 0 {
		l.repos[i].Favourite = on
	}
	l.mu.Unlock()
	return l.save()
}

// Rename sets the name the app shows for a repository.
func (l *Library) Rename(path, name string) error {
	path = Clean(path)
	l.mu.Lock()
	if i := l.find(path); i >= 0 && strings.TrimSpace(name) != "" {
		l.repos[i].Name = strings.TrimSpace(name)
	}
	l.mu.Unlock()
	return l.save()
}

// Recent returns up to n repositories, most recently opened first.
func (l *Library) Recent(n int) []Repo {
	l.mu.Lock()
	out := slices.Clone(l.repos)
	l.mu.Unlock()
	slices.SortStableFunc(out, func(a, b Repo) int { return b.Opened.Compare(a.Opened) })
	return out[:min(n, len(out))]
}

// Favourites returns the favourite repositories by name.
func (l *Library) Favourites() []Repo {
	l.mu.Lock()
	var out []Repo
	for _, r := range l.repos {
		if r.Favourite {
			out = append(out, r)
		}
	}
	l.mu.Unlock()
	slices.SortFunc(out, func(a, b Repo) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return out
}

// Search returns the repositories whose name or path holds every word of
// query, ignoring case: name matches first, then the most recent.
func (l *Library) Search(query string) []Repo {
	words := strings.Fields(strings.ToLower(query))
	l.mu.Lock()
	all := slices.Clone(l.repos)
	l.mu.Unlock()
	type hit struct {
		r    Repo
		name bool
	}
	var hits []hit
	for _, r := range all {
		name, path := strings.ToLower(r.Name), strings.ToLower(r.Path)
		ok, inName := true, len(words) > 0
		for _, w := range words {
			n := strings.Contains(name, w)
			if !n && !strings.Contains(path, w) {
				ok = false
				break
			}
			inName = inName && n
		}
		if ok {
			hits = append(hits, hit{r, inName})
		}
	}
	slices.SortStableFunc(hits, func(a, b hit) int {
		if a.name != b.name {
			if a.name {
				return -1
			}
			return 1
		}
		return b.r.Opened.Compare(a.r.Opened)
	})
	out := make([]Repo, len(hits))
	for i, h := range hits {
		out[i] = h.r
	}
	return out
}

// Groups returns the groups, by name.
func (l *Library) Groups() []Group {
	l.mu.Lock()
	out := make([]Group, len(l.groups))
	for i, g := range l.groups {
		out[i] = Group{Name: g.Name, Paths: slices.Clone(g.Paths)}
	}
	l.mu.Unlock()
	slices.SortFunc(out, func(a, b Group) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return out
}

// SetGroup creates or replaces the group called name; no paths removes it.
func (l *Library) SetGroup(name string, paths []string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("library: a group needs a name")
	}
	clean := make([]string, 0, len(paths))
	for _, p := range paths {
		if p = Clean(p); !slices.Contains(clean, p) {
			clean = append(clean, p)
		}
	}
	l.mu.Lock()
	i := slices.IndexFunc(l.groups, func(g Group) bool { return g.Name == name })
	switch {
	case i >= 0 && len(clean) == 0:
		l.groups = slices.Delete(l.groups, i, i+1)
	case i >= 0:
		l.groups[i].Paths = clean
	case len(clean) > 0:
		l.groups = append(l.groups, Group{Name: name, Paths: clean})
	}
	l.mu.Unlock()
	return l.save()
}

func (l *Library) save() error {
	l.mu.Lock()
	data, err := json.MarshalIndent(file{Repos: l.repos, Groups: l.groups}, "", "  ")
	l.mu.Unlock()
	if err != nil {
		return err
	}
	return settings.WriteFile(l.path, append(data, '\n'))
}
