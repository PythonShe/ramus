// Package fileicon picks a file's icon by its name, as a code editor's
// file list does, from Miguel Solorio's Symbols icon theme (MIT, see
// LICENSE and README.md).
package fileicon

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	_ "embed"
	"io"
	"path"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
)

// The icons, packed by generate.go: unpacked once a file first needs one,
// so that a window without a file list never does.
//
//go:embed icons.tar.gz
var packed []byte

var (
	unpack sync.Once
	svgs   map[string][]byte
	// parsed holds the icons parsed so far: no more than the theme has,
	// a few hundred small paths.
	mu     sync.Mutex
	parsed = map[string]*ui.SVG{}
)

// For is the icon of the file at path, a slash-separated path as git
// gives it: by its whole name, then its extensions, longest first, then
// the language its last one names, else the icon of any file. It is nil
// only when the icons cannot be read.
func For(p string) *ui.SVG {
	return icon(Name(p))
}

// Name is the name of the icon For gives the file at path.
func Name(p string) string {
	base := strings.ToLower(path.Base(p))
	if n, ok := byName[base]; ok {
		return n
	}
	// "a.d.ts" tries "d.ts", then "ts"; a name's leading dot, as in
	// ".env", starts no extension.
	for i := 1; i < len(base); i++ {
		if base[i] != '.' {
			continue
		}
		if n, ok := byExtension[base[i+1:]]; ok {
			return n
		}
	}
	if i := strings.LastIndexByte(base, '.'); i > 0 {
		if n, ok := byLanguage[base[i+1:]]; ok {
			return n
		}
	}
	return defaultIcon
}

func icon(name string) *ui.SVG {
	unpack.Do(func() { svgs = read(packed) })
	mu.Lock()
	defer mu.Unlock()
	if s, ok := parsed[name]; ok {
		return s
	}
	s, err := ui.ParseSVG(svgs[name+".svg"])
	if err != nil {
		s = nil
		if name != defaultIcon {
			mu.Unlock()
			s = icon(defaultIcon)
			mu.Lock()
		}
	}
	parsed[name] = s
	return s
}

// read unpacks the icons, none when they cannot be read.
func read(b []byte) map[string][]byte {
	m := map[string][]byte{}
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return m
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err != nil {
			return m
		}
		if data, err := io.ReadAll(tr); err == nil {
			m[h.Name] = data
		}
	}
}
