// Package statefile reads and writes the files of the app's state (the
// settings, the library, the layout, the drafts, the folders trusted): a
// file is replaced whole and reaches the disk before it counts as written,
// one that does not read is kept aside rather than written over, and what
// a later version of the app wrote in it is kept across a save. It imports
// nothing of the app, so the engine may use it.
package statefile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// LoadError is a file of the app's state (the settings, the library, the
// layout, a draft) that could not be read, or not all of it. Such a file
// is never written over: it is kept aside first, or, when it cannot be,
// left alone and not written at all.
type LoadError struct {
	// Path is the file.
	Path string
	// Err is why it could not be read.
	Err error
	// Partial is set when the rest of the file was read and is in force.
	Partial bool
	// KeptAs is where the file as it was is kept: moved there when none
	// of it was read, copied when the rest was. "" when it is not kept
	// aside: KeepErr says why, or, without one, nothing of it will be
	// lost, as with settings whose file keeps what it could not read.
	KeptAs  string
	KeepErr error
}

func (e *LoadError) Error() string {
	msg := e.Path + ": " + e.Err.Error()
	switch {
	case e.KeptAs != "":
		msg += " (kept as " + e.KeptAs + ")"
	case e.KeepErr != nil:
		msg += " (not kept aside, so not written over: " + e.KeepErr.Error() + ")"
	}
	return msg
}

func (e *LoadError) Unwrap() error { return e.Err }

// maxAside bounds the names tried for a copy kept aside.
const maxAside = 100

// asideName is the n-th name a file kept aside may take beside path.
func asideName(path string, n int) string {
	if n == 0 {
		return path + ".broken"
	}
	return path + ".broken-" + strconv.Itoa(n+1)
}

// keepAside moves the file at path, which could not be read at all, to a
// name beside it that no file has, so that writing path again loses
// nothing, and returns that name. Through a symbolic link it is the file
// the link leads to that moves, beside itself: the link stays, for the
// next write to make that file again.
func keepAside(path string) (string, error) {
	path, err := target(path)
	if err != nil {
		return "", err
	}
	for n := range maxAside {
		name := asideName(path, n)
		if _, err := os.Lstat(name); !errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err := rename(path, name, nil); err != nil {
			return "", err
		}
		return name, nil
	}
	return "", fmt.Errorf("%s: no free name to keep it aside", path)
}

// keepCopy keeps data, the content of the file at path, part of which
// could not be read, in a file beside it, and returns its name: one that
// holds the same already, else a new one. Read again at each launch
// until a save replaces it, the same file is kept once. Through a
// symbolic link it is kept beside the file the link leads to.
func keepCopy(path string, data []byte) (string, error) {
	path, err := target(path)
	if err != nil {
		return "", err
	}
	for n := range maxAside {
		name := asideName(path, n)
		old, err := os.ReadFile(name)
		if err == nil && bytes.Equal(old, data) {
			return name, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err := WriteFile(name, data); err != nil {
			return "", err
		}
		return name, nil
	}
	return "", fmt.Errorf("%s: no free name to keep a copy", path)
}

// Extra returns the keys of obj, a JSON object, that no field of t (a
// struct) names, as encoding/json matches them: those a later version of
// the app wrote, for a save to keep. Nil when there are none or obj is
// no object.
func Extra(obj []byte, t reflect.Type) map[string]json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(obj, &m) != nil {
		return nil
	}
	names := fieldNames(t, nil)
	for k := range m {
		if slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(n, k) }) {
			delete(m, k)
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// fieldNames appends the JSON names of t's fields to names, those of
// embedded structs among them.
func fieldNames(t reflect.Type, names []string) []string {
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if f.Anonymous && tag == "" && f.Type.Kind() == reflect.Struct {
			names = fieldNames(f.Type, names)
			continue
		}
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		switch name {
		case "-":
			continue
		case "":
			name = f.Name
		}
		names = append(names, name)
	}
	return names
}

// WithExtra is v as JSON with the keys of extra (Extra) added: what a
// later version wrote, kept across a save of this one.
func WithExtra(v any, extra map[string]json.RawMessage) (json.RawMessage, error) {
	data, err := json.Marshal(v)
	if err != nil || len(extra) == 0 {
		return data, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	for k, x := range extra {
		if _, ok := m[k]; !ok {
			m[k] = x
		}
	}
	return json.Marshal(m)
}

// KeepUnknown is now, the JSON of a value of type t that replaces old,
// with the keys of old that this version does not know added back, at
// every level: in an object of a struct, in the entries of a map, by key,
// and in the elements of a list, by their key field where their struct
// has one (tagged statefile:"key", as an action's name), else by place.
// What a later version added inside a value this one rewrites is so kept.
// A key this version knows is now's to leave out, as an entry of a map or
// an element of a list is. Without such keys, now comes back as it is.
//
// By place, an element removed or moved before others hands its keys to
// the element that takes its place: a list of structs the app rewrites
// against what the file held needs a key field, which a test of each
// state file's types asks for.
func KeepUnknown(old, now json.RawMessage, t reflect.Type) json.RawMessage {
	out, _ := keepUnknown(old, now, t)
	return out
}

func keepUnknown(old, now json.RawMessage, t reflect.Type) (json.RawMessage, bool) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		var o, n map[string]json.RawMessage
		if json.Unmarshal(old, &o) != nil || json.Unmarshal(now, &n) != nil || o == nil || n == nil {
			return now, false
		}
		changed := false
		var fields func(t reflect.Type)
		fields = func(t reflect.Type) {
			for i := range t.NumField() {
				f := t.Field(i)
				tag := f.Tag.Get("json")
				// Its fields are written at this level, as fieldNames has it.
				if f.Anonymous && tag == "" && f.Type.Kind() == reflect.Struct {
					fields(f.Type)
					continue
				}
				name, _, _ := strings.Cut(tag, ",")
				if !f.IsExported() || name == "-" {
					continue
				}
				if name == "" {
					name = f.Name
				}
				nk, nv, ok := LookupKey(n, name)
				_, ov, found := LookupKey(o, name)
				if !ok || !found {
					continue
				}
				if v, c := keepUnknown(ov, nv, f.Type); c {
					n[nk], changed = v, true
				}
			}
		}
		fields(t)
		for k, v := range Extra(old, t) {
			if _, ok := n[k]; !ok {
				n[k], changed = v, true
			}
		}
		return remarshal(n, now, changed)
	case reflect.Map:
		var o, n map[string]json.RawMessage
		if t.Key().Kind() != reflect.String || json.Unmarshal(old, &o) != nil || json.Unmarshal(now, &n) != nil {
			return now, false
		}
		changed := false
		for k, nv := range n {
			if ov, ok := o[k]; ok {
				if v, c := keepUnknown(ov, nv, t.Elem()); c {
					n[k], changed = v, true
				}
			}
		}
		return remarshal(n, now, changed)
	case reflect.Slice, reflect.Array:
		var o, n []json.RawMessage
		if t.Elem().Kind() == reflect.Uint8 || json.Unmarshal(old, &o) != nil || json.Unmarshal(now, &n) != nil {
			return now, false
		}
		key := keyField(t.Elem())
		used := make([]bool, len(o))
		changed := false
		for i := range n {
			j := -1
			switch {
			case key == "" && i < len(o):
				j = i
			case key != "":
				for m := range o {
					if !used[m] && sameKey(o[m], n[i], key) {
						j = m
						break
					}
				}
			}
			if j < 0 {
				continue
			}
			used[j] = true
			if v, c := keepUnknown(o[j], n[i], t.Elem()); c {
				n[i], changed = v, true
			}
		}
		return remarshal(n, now, changed)
	}
	return now, false
}

// keyField is the JSON name of the field of t, a struct, tagged
// statefile:"key", which tells its elements in a list apart; "" none.
func keyField(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return ""
	}
	for i := range t.NumField() {
		f := t.Field(i)
		if f.Tag.Get("statefile") == "key" {
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "" {
				name = f.Name
			}
			return name
		}
	}
	return ""
}

// sameKey reports whether the objects a and b have the same value under
// key.
func sameKey(a, b json.RawMessage, key string) bool {
	var oa, ob map[string]json.RawMessage
	if json.Unmarshal(a, &oa) != nil || json.Unmarshal(b, &ob) != nil {
		return false
	}
	_, ra, okA := LookupKey(oa, key)
	_, rb, okB := LookupKey(ob, key)
	if !okA || !okB {
		return false
	}
	var va, vb any
	return json.Unmarshal(ra, &va) == nil && json.Unmarshal(rb, &vb) == nil && reflect.DeepEqual(va, vb)
}

// remarshal is v as JSON when changed, else now.
func remarshal(v any, now json.RawMessage, changed bool) (json.RawMessage, bool) {
	if !changed {
		return now, false
	}
	out, err := json.Marshal(v)
	if err != nil {
		return now, false
	}
	return out, true
}

// Unreadable is the LoadError of the file at path, which could not be
// read at all: it is moved aside.
func Unreadable(path string, err error) *LoadError {
	e := &LoadError{Path: path, Err: err}
	e.KeptAs, e.KeepErr = keepAside(path)
	return e
}

// PartlyRead is the LoadError of the file at path, holding data, of which
// the rest was read: a copy of it is kept aside.
func PartlyRead(path string, data []byte, err error) *LoadError {
	e := &LoadError{Path: path, Err: err, Partial: true}
	e.KeptAs, e.KeepErr = keepCopy(path, data)
	return e
}

// LookupKey finds name in top as encoding/json does: as written, else
// in another case.
func LookupKey(top map[string]json.RawMessage, name string) (string, json.RawMessage, bool) {
	if name == "" {
		return "", nil, false
	}
	if v, ok := top[name]; ok {
		return name, v, true
	}
	for k, v := range top {
		if strings.EqualFold(k, name) {
			return k, v, true
		}
	}
	return "", nil, false
}

// ErrChanged is a file found changed since it was read, at its
// replacement by WriteFileIf.
var ErrChanged = errors.New("changed meanwhile")

// WriteFile replaces path with data through a temporary file beside it.
// The data reaches the disk before the rename: without that, a power cut
// soon after can leave the file empty on file systems that order neither,
// and an empty file is one the app cannot read.
//
// A path that is a symbolic link is written through it: the file the link
// leads to is replaced, through a temporary file beside that file, and
// the link stays. A link that leads nowhere has its file made, in a
// folder that must exist already; a loop of links, or a file the link
// leads to in a folder that cannot be written, fails the write and leaves
// everything as it was.
func WriteFile(path string, data []byte) error { return WriteFileIf(path, data, nil) }

// WriteFileIf is WriteFile, which, with unchanged, replaces the file only
// while unchanged says it is as it was, asked before each try of the
// rename, and fails with ErrChanged when it is not.
func WriteFileIf(path string, data []byte, unchanged func() bool) error {
	to, err := target(path)
	if err != nil {
		return err
	}
	dir := filepath.Dir(to)
	if to == path {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	} else if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("%s leads to %s, in a folder that cannot be reached: %w", path, to, err)
	}
	path = to
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := rename(tmp.Name(), path, unchanged); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	syncDir(dir)
	return nil
}
