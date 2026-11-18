package repo

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"ramus/internal/git/config"
	"ramus/internal/git/index"
)

// DependencyFolders are the folders package managers, virtual
// environments and tools' caches fill, which a repository almost never
// means to keep, for HideFolders. Names that are as often kept on
// purpose (vendor, Pods, .yarn) or are a build's output (build, dist,
// target, out) are not among them.
var DependencyFolders = []string{
	"node_modules", "bower_components", "jspm_packages", ".pnpm-store",
	".venv", "venv", "__pycache__", ".pytest_cache", ".mypy_cache", ".ruff_cache", ".tox", ".nox", ".ipynb_checkpoints",
	".next", ".nuxt", ".svelte-kit", ".turbo", ".parcel-cache", ".angular", ".expo",
	".gradle", ".dart_tool", ".terraform", ".build", "DerivedData",
}

// hiddenFolders is what HideFolders keeps between calls, to do nothing
// when nothing changed.
type hiddenFolders struct {
	mu sync.Mutex
	// names were asked for last; shown are those of them the index
	// tracks a file in, read when the index held entries entries: a name
	// comes to be tracked as entries are added, and scanning a large
	// index on every change, as staging a file makes, costs.
	names   []string
	shown   []string
	entries int64
	// file is the excludes file git is given, "" until one is made, and
	// content what it holds, "" while it is not given.
	file, content string
}

// HideFolders has every command of the repository treat the folders
// named names, at any depth, as ignored, as if the user's
// core.excludesFile listed them besides its own rules: git status leaves
// them out, as does git add of a folder that holds one, and git clean
// keeps them. A name the index tracks a file under is left as it is:
// a repository keeping a folder means new files in it to show. None
// puts back the user's own rules alone.
//
// It does nothing when the names, the index and the user's excludes file
// are as they were, so it is called before each status read; it reports
// whether what git leaves out changed. Off the main thread.
func (r *Repository) HideFolders(names []string) (changed bool, err error) {
	h := &r.hidden
	h.mu.Lock()
	defer h.mu.Unlock()
	if r.WorkTree == "" {
		return false, nil
	}
	if !slices.Equal(names, h.names) {
		h.names, h.entries = slices.Clone(names), -2
	}
	if len(names) > 0 {
		if n := indexEntries(filepath.Join(r.GitDir, "index")); n != h.entries {
			var ok bool
			if h.shown, ok = r.trackedNames(names); ok {
				h.entries = n
			} else {
				// Read again next time: an index being replaced, as on
				// Windows, can fail a read. Till then nothing is hidden.
				h.entries = -2
			}
		}
	}
	var hide []string
	for _, n := range names {
		if !slices.Contains(h.shown, n) {
			hide = append(hide, n)
		}
	}
	var content string
	if len(hide) > 0 {
		// The user's file is read again each time: it is small, and git
		// gives no other sign it changed.
		var ok bool
		if content, ok = r.excludesWith(hide); !ok {
			hide = nil
		}
	}
	if len(hide) == 0 {
		changed = h.content != ""
		h.content = ""
		r.Runner.SetConfig(nil)
		return changed, nil
	}
	if content == h.content && h.file != "" && stamp(h.file).size == int64(len(content)) {
		return false, nil
	}
	if err := h.write(r, content); err != nil {
		return false, err
	}
	r.Runner.SetConfig([]string{"core.excludesFile=" + filepath.ToSlash(h.file)})
	return true, nil
}

// excludesWith is the user's excludes file followed by rules ignoring
// the folders named hide, but those the user's own rules take back
// (!node_modules/), which later rules would otherwise win over. It is
// not ok where the user's file cannot be copied as git reads it, its
// path being one the engine does not resolve or the file too large:
// given in its place, the copy would drop the user's own rules.
func (r *Repository) excludesWith(hide []string) (string, bool) {
	source, ok := r.userExcludes()
	if !ok {
		return "", false
	}
	var own []byte
	if source != "" {
		fi, err := os.Stat(source)
		if err == nil && fi.Size() > maxExcludes {
			return "", false
		}
		own, _ = os.ReadFile(source) // git reads one it cannot as empty
	}
	var b strings.Builder
	b.Write(own)
	if len(own) > 0 && own[len(own)-1] != '\n' {
		b.WriteByte('\n')
	}
	b.WriteString("# Ramus: dependency folders, hidden from its commands and what they run\n")
	for _, n := range hide {
		if !takenBack(own, n) {
			b.WriteString(n)
			b.WriteString("/\n")
		}
	}
	return b.String(), true
}

// takenBack reports whether rules un-ignore the folder name, at any
// depth: !name, !name/, !**/name or !/name, with or without /**.
func takenBack(rules []byte, name string) bool {
	for _, line := range bytes.Split(rules, []byte("\n")) {
		l := string(bytes.TrimSpace(line))
		l, ok := strings.CutPrefix(l, "!")
		if !ok {
			continue
		}
		l = strings.TrimPrefix(strings.TrimPrefix(l, "**/"), "/")
		l = strings.TrimSuffix(strings.TrimSuffix(l, "/**"), "/")
		if l == name {
			return true
		}
	}
	return false
}

// maxExcludes bounds the user's excludes file copied; git reads one of
// any size, but a larger one is no list of rules.
const maxExcludes = 4 << 20

// write puts content in the repository's excludes file, made the first
// time and removed when the repository closes: written beside it and
// renamed over it, so that git never reads it half written.
func (h *hiddenFolders) write(r *Repository, content string) error {
	if h.file == "" {
		f, err := os.CreateTemp("", "ramus-exclude-*")
		if err != nil {
			return err
		}
		name := f.Name()
		f.Close()
		if _, err := r.life.Own(removeFile(name)); err != nil {
			os.Remove(name)
			return err
		}
		h.file = name
	}
	tmp := h.file + ".new"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, h.file); err != nil {
		os.Remove(tmp)
		return err
	}
	h.content = content
	return nil
}

// removeFile removes a file when closed.
type removeFile string

func (f removeFile) Close() error {
	err := os.Remove(string(f))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// indexEntries is the number of entries the index at path says it
// holds, -1 for none or one that cannot be read.
func indexEntries(path string) int64 {
	f, err := os.Open(path)
	if err != nil {
		return -1
	}
	defer f.Close()
	var head [12]byte
	if _, err := io.ReadFull(f, head[:]); err != nil || string(head[:4]) != "DIRC" {
		return -1
	}
	return int64(binary.BigEndian.Uint32(head[8:]))
}

// trackedNames returns those of names that the index tracks a file
// under, at any depth. An index the engine cannot read whole (split, or
// past its size) counts as tracking them all, so that nothing is hidden
// that might be kept; one that fails to read is not ok, to read again.
func (r *Repository) trackedNames(names []string) ([]string, bool) {
	if r.Caps.SplitIndex {
		return names, true
	}
	x, err := index.ReadFile(filepath.Join(r.GitDir, "index"), r.Caps.HashSize)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, true // nothing is tracked yet
	case errors.Is(err, index.ErrFormat):
		return names, true
	case err != nil:
		return names, false
	case x.Split():
		return names, true
	}
	want := make(map[string]bool, len(names))
	most := 0
	for _, n := range names {
		want[n] = true
		most = max(most, len(n))
	}
	var found []string
	for i := range x.Entries {
		p := x.Path(i)
		if x.Entries[i].SparseDir() {
			// A sparse index's folder: its name ends it, slash and all.
			p = bytes.TrimSuffix(p, []byte("/"))
		} else if j := bytes.LastIndexByte(p, '/'); j >= 0 {
			p = p[:j] // the folders holding the file
		} else {
			continue
		}
		for len(p) > 0 {
			name := p
			j := bytes.IndexByte(p, '/')
			if j >= 0 {
				name, p = p[:j], p[j+1:]
			} else {
				p = nil
			}
			if len(name) <= most && want[string(name)] && !slices.Contains(found, string(name)) {
				found = append(found, string(name))
				if len(found) == len(names) {
					return found, true
				}
			}
		}
	}
	return found, true
}

// userExcludes is the path of the user's own excludes file, as git
// finds it: core.excludesFile, else git/ignore under XDG_CONFIG_HOME or
// ~/.config; "" for none. It is not ok for a path the engine does not
// resolve as git does (~user/, %(prefix)/).
func (r *Repository) userExcludes() (string, bool) {
	env := func(k string) string {
		for _, kv := range r.Runner.Environ() {
			if v, ok := strings.CutPrefix(kv, k+"="); ok {
				return v
			}
		}
		return os.Getenv(k)
	}
	home := env("HOME")
	if home == "" {
		home = env("USERPROFILE")
	}
	// The last value set but the one HideFolders gives, which the
	// config, read through the repository's runner, holds too.
	own := filepath.ToSlash(r.hidden.file)
	entries := r.Config().Entries()
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.Key != "core.excludesfile" || e.Scope == config.Command && own != "" && e.Value == own {
			continue
		}
		p := e.Value
		switch {
		case p == "":
			return "", true
		case p == "~" || strings.HasPrefix(p, "~/"):
			return filepath.Join(home, filepath.FromSlash(strings.TrimPrefix(p[1:], "/"))), true
		case strings.HasPrefix(p, "~") || strings.HasPrefix(p, "%("):
			return "", false
		case filepath.IsAbs(p):
			return p, true
		}
		// git reads a relative path from where it runs: the work tree.
		return filepath.Join(r.WorkTree, filepath.FromSlash(p)), true
	}
	if x := env("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "git", "ignore"), true
	}
	if home == "" {
		return "", true
	}
	return filepath.Join(home, ".config", "git", "ignore"), true
}
