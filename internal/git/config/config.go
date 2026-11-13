// Package config reads a repository's configuration through git itself,
// in one git config --list call, so that includes, conditional includes,
// worktree config and every scope resolve exactly as git resolves them.
// It parses only git's -z output, never a config file.
package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"ramus/internal/git/run"
)

// Scope is where an entry was set.
type Scope string

const (
	System   Scope = "system"
	Global   Scope = "global"
	Local    Scope = "local"
	Worktree Scope = "worktree"
	Command  Scope = "command"
)

// Entry is one key=value line.
type Entry struct {
	Key    string // lower-cased section and name, as git prints them
	Value  string
	NoEq   bool // the key was written without "=", which git reads as true
	Scope  Scope
	Origin string // "file:/path", "command line:", ...
}

// Config is a snapshot of every entry, in git's order: later entries win.
type Config struct {
	entries []Entry
	// UnknownExtensions are repository extensions this git does not know.
	// git then leaves the repository's own config out of the list, and
	// refuses to work in it, so the repository is out of reach entirely.
	UnknownExtensions []string
}

// Read runs git config --list for the repository.
func Read(ctx context.Context, r *run.Runner, gitDir string) (*Config, error) {
	res, err := r.Run(ctx, &run.Cmd{
		GitDir: gitDir, Read: true,
		Args: []string{"config", "--list", "-z", "--show-origin", "--show-scope"},
	})
	if err != nil {
		var e *run.Error
		// An empty config is an error exit in some versions; read nothing.
		if errors.As(err, &e) && e.ExitCode == 1 && len(res.Stdout) == 0 {
			return &Config{}, nil
		}
		return nil, err
	}
	c, err := Parse(res.Stdout)
	if err != nil {
		return nil, err
	}
	c.UnknownExtensions = unknownExtensions(res.Stderr)
	return c, nil
}

// unknownExtensions reads git's warning:
//
//	warning: unknown repository extension found:
//		somethingnew
func unknownExtensions(stderr string) []string {
	var out []string
	in := false
	for _, line := range strings.Split(stderr, "\n") {
		switch {
		case strings.Contains(line, "unknown repository extension"):
			in = true
		case in && strings.HasPrefix(line, "\t"):
			out = append(out, strings.TrimSpace(line))
		default:
			in = false
		}
	}
	return out
}

// Parse reads the output of git config --list -z --show-origin
// --show-scope.
func Parse(out []byte) (*Config, error) {
	c := &Config{}
	for len(out) > 0 {
		var fields [3][]byte
		for i := range fields {
			j := bytes.IndexByte(out, 0)
			if j < 0 {
				return nil, fmt.Errorf("config: entry %d cut short", len(c.entries))
			}
			fields[i] = out[:j]
			out = out[j+1:]
		}
		e := Entry{Scope: Scope(fields[0]), Origin: string(fields[1])}
		k, v, ok := bytes.Cut(fields[2], []byte{'\n'})
		e.Key, e.Value, e.NoEq = string(k), string(v), !ok
		c.entries = append(c.entries, e)
	}
	return c, nil
}

// Entries returns every entry in order.
func (c *Config) Entries() []Entry { return c.entries }

// normalize lower-cases a key's section and name but keeps a subsection's
// case, as git compares them.
func normalize(key string) string {
	first := strings.IndexByte(key, '.')
	last := strings.LastIndexByte(key, '.')
	if first < 0 {
		return strings.ToLower(key)
	}
	return strings.ToLower(key[:first]) + key[first:last] + strings.ToLower(key[last:])
}

// Lookup returns the last entry for key.
func (c *Config) Lookup(key string) (Entry, bool) {
	key = normalize(key)
	for i := len(c.entries) - 1; i >= 0; i-- {
		if c.entries[i].Key == key {
			return c.entries[i], true
		}
	}
	return Entry{}, false
}

// Get returns the last value for key.
func (c *Config) Get(key string) (string, bool) {
	e, ok := c.Lookup(key)
	return e.Value, ok
}

// All returns every value of a multi-valued key, in order.
func (c *Config) All(key string) []string {
	key = normalize(key)
	var vs []string
	for _, e := range c.entries {
		if e.Key == key {
			vs = append(vs, e.Value)
		}
	}
	return vs
}

// Bool reads key as git reads a boolean, def when unset or unreadable.
func (c *Config) Bool(key string, def bool) bool {
	e, ok := c.Lookup(key)
	if !ok {
		return def
	}
	if e.NoEq {
		return true
	}
	switch strings.ToLower(e.Value) {
	case "true", "yes", "on":
		return true
	case "false", "no", "off", "":
		return false
	}
	if n, err := strconv.Atoi(e.Value); err == nil {
		return n != 0
	}
	return def
}

// Int reads key as an integer with git's k, m and g suffixes.
func (c *Config) Int(key string, def int64) int64 {
	v, ok := c.Get(key)
	if !ok || v == "" {
		return def
	}
	mult := int64(1)
	switch v[len(v)-1] {
	case 'k', 'K':
		mult, v = 1<<10, v[:len(v)-1]
	case 'm', 'M':
		mult, v = 1<<20, v[:len(v)-1]
	case 'g', 'G':
		mult, v = 1<<30, v[:len(v)-1]
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	return n * mult
}

// Subsections returns the subsection names of a section that have key
// set, such as the remotes with a promisor flag.
func (c *Config) Subsections(section, key string) []string {
	section, key = strings.ToLower(section), strings.ToLower(key)
	seen := map[string]bool{}
	var out []string
	for _, e := range c.entries {
		if !strings.HasPrefix(e.Key, section+".") || !strings.HasSuffix(e.Key, "."+key) {
			continue
		}
		sub := e.Key[len(section)+1 : len(e.Key)-len(key)-1]
		if sub != "" && !seen[sub] {
			seen[sub] = true
			out = append(out, sub)
		}
	}
	return out
}
