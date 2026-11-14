// Package licenses holds the license of Ramus and of everything built into
// it, for the About window. generate.go writes them from the build's
// modules; never edit licenses.json.gz.
package licenses

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"io"
)

//go:embed licenses.json.gz
var packed []byte

// Notice is the license of one part of the app.
type Notice struct {
	// Name is the part's: "Ramus", "Go", a module's path, or the icons'.
	Name string `json:"name"`
	// Version is the part's version, "" for Ramus's own.
	Version string `json:"version,omitempty"`
	// Kind is the license's SPDX identifier when it is a common one.
	Kind string `json:"kind,omitempty"`
	// Text is the license in full, with any notice and patent grant.
	Text string `json:"text"`
}

// All are the licenses: Ramus's first, then Go's, the modules', and the
// file icons'. They are unpacked at each call, which only the About
// window makes.
func All() ([]Notice, error) {
	zr, err := gzip.NewReader(bytes.NewReader(packed))
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		return nil, err
	}
	var out []Notice
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}
