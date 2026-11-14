//go:build ignore

// Generates licenses.json.gz: the license of Ramus and of everything built
// into it, as the About window lists them. Those are Go itself, the
// modules the app links on macOS, Linux and Windows, and the file icons.
// Run it from the repository's root after a dependency changes, which
// TestCoversTheBuild reports:
//
//	go run internal/licenses/generate.go
package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type notice struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Text    string `json:"text"`
}

func main() {
	var out []notice
	ramus, err := os.ReadFile("LICENSE")
	check(err)
	out = append(out, notice{Name: "Ramus", Text: string(ramus)})

	root := goEnv("GOROOT")
	gotext := readFirst(filepath.Join(root, "LICENSE"), filepath.Join(root, "..", "LICENSE"))
	if gotext == "" {
		fail("no LICENSE in %s or beside it", root)
	}
	out = append(out, notice{Name: "Go", Version: strings.TrimPrefix(goEnv("GOVERSION"), "go"), Text: join(gotext, readFirst(filepath.Join(root, "PATENTS")))})

	// The modules linked, and the folders within them of the packages
	// linked, some of which have a license of their own, as a port's.
	type module struct {
		version, dir string
		pkgs         map[string]bool
	}
	mods := map[string]*module{}
	for _, goos := range []string{"darwin", "linux", "windows"} {
		cmd := exec.Command("go", "list", "-deps", "-f", "{{with .Module}}{{if not .Main}}{{.Path}}\t{{.Version}}\t{{.Dir}}{{end}}{{end}}\t{{.Dir}}", ".")
		cmd.Env = append(os.Environ(), "GOOS="+goos)
		cmd.Stderr = os.Stderr
		b, err := cmd.Output()
		check(err)
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			f := strings.Split(line, "\t")
			if len(f) != 4 {
				continue
			}
			m := mods[f[0]]
			if m == nil {
				m = &module{version: strings.TrimPrefix(f[1], "v"), dir: f[2], pkgs: map[string]bool{}}
				mods[f[0]] = m
			}
			m.pkgs[f[3]] = true
		}
	}
	paths := make([]string, 0, len(mods))
	for p := range mods {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		m := mods[p]
		text := moduleLicense(m.dir, "")
		if text == "" {
			fail("%s %s has no license file in %s", p, m.version, m.dir)
		}
		// Within the module, the folders of the packages linked and those
		// between them and its root, each once.
		seen := map[string]bool{m.dir: true}
		var dirs []string
		for pkg := range m.pkgs {
			for d := pkg; strings.HasPrefix(d, m.dir+string(filepath.Separator)) && !seen[d]; d = filepath.Dir(d) {
				seen[d] = true
				dirs = append(dirs, d)
			}
		}
		sort.Strings(dirs)
		for _, d := range dirs {
			rel, err := filepath.Rel(m.dir, d)
			check(err)
			text = join(text, moduleLicense(d, filepath.ToSlash(rel)+"/"))
		}
		out = append(out, notice{Name: p, Version: m.version, Text: text})
	}

	icons, err := os.ReadFile(filepath.Join("internal", "ui", "fileicon", "LICENSE"))
	check(err)
	tables, err := os.ReadFile(filepath.Join("internal", "ui", "fileicon", "tables_gen.go"))
	check(err)
	version := ""
	if m := regexp.MustCompile(`from Symbols (\S+);`).FindSubmatch(tables); m != nil {
		version = string(m[1])
	}
	out = append(out, notice{Name: "Symbols file icons", Version: version, Text: string(icons)})

	for i := range out {
		out[i].Kind = kind(out[i].Text)
	}
	b, err := json.Marshal(out)
	check(err)
	var packed bytes.Buffer
	zw, err := gzip.NewWriterLevel(&packed, gzip.BestCompression)
	check(err)
	_, err = zw.Write(b)
	check(err)
	check(zw.Close())
	check(os.WriteFile(filepath.Join("internal", "licenses", "licenses.json.gz"), packed.Bytes(), 0o644))
	fmt.Printf("%d licenses, %d bytes packed\n", len(out), packed.Len())
}

// moduleLicense is the text of the license files in dir, its license
// first, then any notice and patent grant; each headed by its path, as
// prefix and its name, when prefix is not "".
func moduleLicense(dir, prefix string) string {
	entries, err := os.ReadDir(dir)
	check(err)
	var lic, rest []string
	for _, e := range entries {
		name := strings.ToUpper(e.Name())
		if e.IsDir() {
			continue
		}
		var into *[]string
		switch {
		case strings.HasPrefix(name, "LICENSE"), strings.HasPrefix(name, "LICENCE"), strings.HasPrefix(name, "COPYING"):
			into = &lic
		case strings.HasPrefix(name, "NOTICE"), name == "PATENTS":
			into = &rest
		default:
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		check(err)
		text := string(b)
		if prefix != "" {
			text = prefix + e.Name() + ":\n\n" + text
		}
		*into = append(*into, text)
	}
	if len(lic) == 0 {
		return ""
	}
	return join(append(lic, rest...)...)
}

// kind names the license a text is by its wording, "" when it is none of
// the common ones.
func kind(text string) string {
	if m := regexp.MustCompile(`SPDX-License-Identifier:[ \t]*([^\r\n]+)`).FindStringSubmatch(text); m != nil {
		return strings.TrimSpace(m[1])
	}
	switch {
	case strings.Contains(text, "Apache License") && strings.Contains(text, "Version 2.0"):
		return "Apache-2.0"
	case strings.Contains(text, "Permission is hereby granted, free of charge"):
		return "MIT"
	case strings.Contains(text, "Redistribution and use in source and binary forms") && strings.Contains(text, "Neither the name"):
		return "BSD-3-Clause"
	case strings.Contains(text, "Redistribution and use in source and binary forms"):
		return "BSD-2-Clause"
	}
	return ""
}

func join(texts ...string) string {
	var parts []string
	for _, t := range texts {
		if t = strings.TrimSpace(t); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n\n")
}

func readFirst(paths ...string) string {
	for _, p := range paths {
		if b, err := os.ReadFile(p); err == nil {
			return string(b)
		}
	}
	return ""
}

func goEnv(key string) string {
	b, err := exec.Command("go", "env", key).Output()
	check(err)
	return strings.TrimSpace(string(b))
}

func check(err error) {
	if err != nil {
		fail("%v", err)
	}
}

func fail(f string, a ...any) {
	fmt.Fprintf(os.Stderr, f+"\n", a...)
	os.Exit(1)
}
