package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ramus/internal/session"
)

// openTimes opens dir and times its refs, its status, its branches and
// tags as the branches view reads them, and its history.
func openTimes(m *meter, dir string, prefix string) error {
	start := time.Now()
	s, p, err := open(dir)
	if err != nil {
		return err
	}
	defer p.close()
	if err := p.until("the refs", 2*time.Minute, func() bool { return s.State.Refs != nil }); err != nil {
		return err
	}
	m.time(prefix+"open to refs read", time.Since(start))
	if err := p.until("the status", 2*time.Minute, func() bool { return s.State.Status != nil }); err != nil {
		return err
	}
	m.time(prefix+"open to status", time.Since(start))
	t0 := time.Now()
	var info *session.RefInfo
	s.LoadRefInfo(func(ri session.RefInfo) { info = &ri })
	if err := p.until("the branches", 2*time.Minute, func() bool { return info != nil }); err != nil {
		return err
	}
	if info.Err != nil {
		return info.Err
	}
	m.time(prefix+"branches view read", time.Since(t0))
	m.fact(prefix+"branches", len(info.Branches))
	t1 := time.Now()
	s.LoadHistory()
	first := time.Duration(0)
	if err := p.until("the history", 5*time.Minute, func() bool {
		if first == 0 && s.History.Snap != nil && s.History.Snap.Len() > 0 {
			first = time.Since(t1)
			m.time(prefix+"history first screen", first)
		}
		return s.History.Done
	}); err != nil {
		return err
	}
	if s.History.Err != nil {
		return s.History.Err
	}
	m.time(prefix+"whole history", time.Since(t1))
	m.fact(prefix+"commits", s.History.Snap.Len())
	m.fact(prefix+"native", s.History.Native)
	return nil
}

// refs50k is a repository of 50,000 branches, packed.
func refs50k(m *meter) error {
	dir, err := made("refs-50k", func(dir string) error {
		if err := fastImport(dir, 1000, 500, 2); err != nil {
			return err
		}
		work := repoDir(dir)
		out, err := git(work, nil, "rev-list", "main")
		if err != nil {
			return err
		}
		ids := strings.Fields(out)
		var b bytes.Buffer
		for i := range 50000 {
			fmt.Fprintf(&b, "create refs/heads/topic/%05d %s\n", i, ids[i%len(ids)])
		}
		if _, err := git(work, b.Bytes(), "update-ref", "--stdin"); err != nil {
			return err
		}
		_, err = git(work, nil, "pack-refs", "--all")
		return err
	})
	if err != nil {
		return err
	}
	return openTimes(m, repoDir(dir), "")
}

// packs2000 is a repository of 2,000 packs and 5,000 loose objects, as one
// fetched often and never repacked.
func packs2000(m *meter) error {
	dir, err := made("packs-2000", func(dir string) error {
		if err := fastImport(dir, 10, 500, 5); err != nil {
			return err
		}
		work := repoDir(dir)
		for c := range 2000 {
			// Each fast-import writes a pack of its own, however small.
			msg := fmt.Sprintf("packed %d", c)
			data := fmt.Sprintf("pack %d\n", c)
			stream := fmt.Sprintf("commit refs/heads/main\ncommitter Bench <bench@example.com> %d +0000\ndata %d\n%s\nfrom refs/heads/main^0\nM 100644 inline packs/p%04d.txt\ndata %d\n%s\n",
				1800000000+c, len(msg), msg, c%100, len(data), data)
			if _, err := git(work, []byte(stream), "-c", "fastimport.unpackLimit=0", "fast-import", "--quiet"); err != nil {
				return err
			}
		}
		var paths bytes.Buffer
		loose := filepath.Join(dir, "loose")
		os.MkdirAll(loose, 0o755)
		for i := range 5000 {
			p := filepath.Join(loose, fmt.Sprintf("%05d", i))
			os.WriteFile(p, []byte(fmt.Sprintf("loose object %d\n", i)), 0o644)
			paths.WriteString(p + "\n")
		}
		if _, err := git(work, paths.Bytes(), "hash-object", "-w", "--stdin-paths"); err != nil {
			return err
		}
		_, err := git(work, nil, "checkout", "-q", "-f", "main")
		return err
	})
	if err != nil {
		return err
	}
	packs, _ := filepath.Glob(filepath.Join(repoDir(dir), ".git", "objects", "pack", "*.pack"))
	m.fact("packs", len(packs))
	return openTimes(m, repoDir(dir), "")
}

// fallbacks are the repositories the engine hands to git, or reads in
// another way: reftable refs, SHA-256 objects, a partial clone and a
// shallow one, each of the same 3,000 commits.
func fallbacks(m *meter) error {
	base, err := made("fallback-base", func(dir string) error {
		if err := fastImport(dir, 3000, 1000, 3); err != nil {
			return err
		}
		_, err := git(repoDir(dir), nil, "config", "uploadpack.allowFilter", "true")
		return err
	})
	if err != nil {
		return err
	}
	url := "file://" + filepath.ToSlash(repoDir(base))
	kinds := []struct {
		name  string
		build func(dir string) error
	}{
		{"files", func(dir string) error { return fastImport(dir, 3000, 1000, 3) }},
		{"reftable", func(dir string) error { return fastImport(dir, 3000, 1000, 3, "--ref-format=reftable") }},
		{"sha256", func(dir string) error { return fastImport(dir, 3000, 1000, 3, "--object-format=sha256") }},
		{"partial", func(dir string) error {
			_, err := git(dir, nil, "clone", "-q", "--filter=blob:none", url, repoDir(dir))
			return err
		}},
		{"shallow", func(dir string) error {
			_, err := git(dir, nil, "clone", "-q", "--depth=100", url, repoDir(dir))
			return err
		}},
	}
	for _, k := range kinds {
		dir, err := made("fallback-"+k.name, k.build)
		if err != nil {
			return fmt.Errorf("%s: %w", k.name, err)
		}
		if err := openTimes(m, repoDir(dir), k.name+": "); err != nil {
			return fmt.Errorf("%s: %w", k.name, err)
		}
	}
	return nil
}
