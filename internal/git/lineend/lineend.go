// Package lineend finds staged files whose line endings a commit would
// change by surprise: CRLF brought into a file that had only LF, or LF
// and CRLF mixed in one file. It reads the blobs through cat-file, and
// names the settings that decide line endings so the user can fix them.
package lineend

import (
	"bytes"
	"context"
	"strings"

	"ramus/internal/git/catfile"
	"ramus/internal/git/run"
)

// Staged is a staged file to check: its blob in the index and in HEAD,
// nil for a file HEAD does not have.
type Staged struct {
	Path        string
	Head, Index []byte
}

// Finding is a staged file whose line endings stand out.
type Finding struct {
	Path string
	// AddsCRLF is a file that had only LF line endings in HEAD and has
	// CRLF ones staged.
	AddsCRLF bool
	// Mixed is a staged file with both LF and CRLF line endings.
	Mixed bool
	// Attr is what .gitattributes says of it ("text=auto eol=lf"), empty
	// when nothing.
	Attr string
}

// Limits on the check; past them the rest goes unchecked.
const (
	MaxFiles = 2000
	MaxBytes = 256 << 20
)

// binaryPeek is how far git looks for a NUL to call a file binary.
const binaryPeek = 8000

type ends struct{ lf, crlf int }

func count(b []byte) (ends, bool) {
	if bytes.IndexByte(b[:min(len(b), binaryPeek)], 0) >= 0 {
		return ends{}, false
	}
	crlf := bytes.Count(b, []byte("\r\n"))
	return ends{lf: bytes.Count(b, []byte("\n")) - crlf, crlf: crlf}, true
}

// Check reads the blobs of files and returns those that stand out, and
// whether every file was checked.
func Check(cat *catfile.Process, files []Staged) ([]Finding, bool, error) {
	complete := len(files) <= MaxFiles
	files = files[:min(len(files), MaxFiles)]
	var oids [][]byte
	for _, f := range files {
		oids = append(oids, f.Index)
	}
	index := make([]ends, len(files))
	text := make([]bool, len(files))
	read := 0
	err := cat.Read(oids, func(i int, o catfile.Object) error {
		if o.Type != "blob" || o.TooLarge || read+len(o.Data) > MaxBytes {
			complete = false
			return nil
		}
		read += len(o.Data)
		index[i], text[i] = count(o.Data)
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	var out []Finding
	var heads [][]byte
	var asked []int
	for i, f := range files {
		if !text[i] || index[i].crlf == 0 {
			continue
		}
		if index[i].lf > 0 {
			out = append(out, Finding{Path: f.Path, Mixed: true})
			continue
		}
		if f.Head != nil {
			heads = append(heads, f.Head)
			asked = append(asked, i)
		}
	}
	err = cat.Read(heads, func(k int, o catfile.Object) error {
		if o.Type != "blob" || o.TooLarge {
			return nil
		}
		if e, ok := count(o.Data); ok && e.crlf == 0 && e.lf > 0 {
			out = append(out, Finding{Path: files[asked[k]].Path, AddsCRLF: true})
		}
		return nil
	})
	return out, complete, err
}

// Settings are what decides line endings in a repository, for the
// warning to show: core.autocrlf and core.eol as set, and each finding's
// text and eol attributes.
func Settings(ctx context.Context, r *run.Runner, gitDir, workTree string, findings []Finding) (autocrlf, eol string, err error) {
	get := func(key string) string {
		res, err := r.Run(ctx, &run.Cmd{GitDir: gitDir, WorkTree: workTree, Read: true, Args: []string{"config", "--get", key}})
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(res.Stdout))
	}
	autocrlf, eol = get("core.autocrlf"), get("core.eol")
	if len(findings) == 0 {
		return autocrlf, eol, nil
	}
	var in bytes.Buffer
	for _, f := range findings {
		in.WriteString(f.Path)
		in.WriteByte(0)
	}
	res, err := r.Run(ctx, &run.Cmd{GitDir: gitDir, WorkTree: workTree, Read: true, Stdin: &in,
		Args: []string{"check-attr", "-z", "--cached", "--stdin", "text", "eol"}})
	if err != nil {
		return autocrlf, eol, err
	}
	// Output: path NUL attribute NUL value NUL, per path and attribute.
	attrs := map[string][]string{}
	f := bytes.Split(res.Stdout, []byte{0})
	for i := 0; i+2 < len(f); i += 3 {
		v := string(f[i+2])
		if v == "unspecified" {
			continue
		}
		a := string(f[i+1])
		switch v {
		case "set":
		case "unset":
			a = "-" + a
		default:
			a += "=" + v
		}
		attrs[string(f[i])] = append(attrs[string(f[i])], a)
	}
	for i := range findings {
		findings[i].Attr = strings.Join(attrs[findings[i].Path], " ")
	}
	return autocrlf, eol, nil
}
