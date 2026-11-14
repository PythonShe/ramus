package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui/editor"

	"ramus/internal/git/diff"
	"ramus/internal/session"
)

// Repositories the cases make are kept, between the runs of one bench,
// in cacheDir, which the bench removes at its end.
func cacheDir() string {
	if d := os.Getenv("RAMUS_BENCH_CACHE"); d != "" {
		return d
	}
	return filepath.Join(os.TempDir(), "ramus-bench")
}

// gitEnv is a git with no user or system config, for repositories made.
func gitEnv() []string {
	return append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_AUTHOR_NAME=Bench", "GIT_AUTHOR_EMAIL=bench@example.com",
		"GIT_COMMITTER_NAME=Bench", "GIT_COMMITTER_EMAIL=bench@example.com", "GIT_TERMINAL_PROMPT=0")
}

func git(dir string, stdin []byte, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir, cmd.Env = dir, gitEnv()
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// made returns the repository name, making it with build the first time
// in this bench; a partly made one is made again.
var madeMu sync.Mutex

func made(name string, build func(dir string) error) (string, error) {
	madeMu.Lock()
	defer madeMu.Unlock()
	dir := filepath.Join(cacheDir(), name)
	if _, err := os.Stat(filepath.Join(dir, ".made")); err == nil {
		return dir, nil
	}
	os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := build(dir); err != nil {
		return "", err
	}
	return dir, os.WriteFile(filepath.Join(dir, ".made"), nil, 0o644)
}

// fastImport makes a repository in dir of commits commits, each changing
// perCommit of files files, through git fast-import. initArgs go to git
// init.
func fastImport(dir string, commits, files, perCommit int, initArgs ...string) error {
	work := filepath.Join(dir, "repo")
	if _, err := git(dir, nil, append([]string{"init", "-q", "-b", "main"}, append(initArgs, work)...)...); err != nil {
		return err
	}
	var b bytes.Buffer
	when := int64(1700000000)
	for c := range commits {
		msg := fmt.Sprintf("change %d", c)
		fmt.Fprintf(&b, "commit refs/heads/main\ncommitter Bench <bench@example.com> %d +0000\ndata %d\n%s\n", when+int64(c)*60, len(msg), msg)
		n := perCommit
		if c == 0 {
			n = files
		}
		for k := range n {
			f := (c*perCommit + k) % files
			data := fmt.Sprintf("file %d, change %d\n", f, c)
			fmt.Fprintf(&b, "M 100644 inline dir%03d/file%05d.txt\ndata %d\n%s\n", f%100, f, len(data), data)
		}
	}
	if _, err := git(work, b.Bytes(), "fast-import", "--quiet"); err != nil {
		return err
	}
	_, err := git(work, nil, "checkout", "-q", "-f", "main")
	return err
}

func repoDir(dir string) string { return filepath.Join(dir, "repo") }

// commit10k times reading the file list of a commit of 10,000 files.
func commit10k(m *meter) error {
	dir, err := made("commit-10k", func(dir string) error { return fastImport(dir, 2, 10000, 10000) })
	if err != nil {
		return err
	}
	s, p, err := open(repoDir(dir))
	if err != nil {
		return err
	}
	defer p.close()
	if err := p.until("the refs", time.Minute, func() bool { return s.State.Refs != nil && s.State.Head.OID != nil }); err != nil {
		return err
	}
	t0 := time.Now()
	var d session.CommitDetail
	s.LoadCommit(s.State.Head.OID, func(cd session.CommitDetail) { d = cd })
	if err := p.until("the files", time.Minute, func() bool { return d.Files != nil || d.Err != nil }); err != nil {
		return err
	}
	if d.Err != nil {
		return d.Err
	}
	m.time("file list", time.Since(t0))
	m.fact("files", d.Files.Len())
	return nil
}

// bigDiff times a 50 MB text file's patch, git's part and the viewer's
// apart, and a 100 MB binary's.
func bigDiff(m *meter) error {
	dir, err := made("big-diff", func(dir string) error {
		work := repoDir(dir)
		if _, err := git(dir, nil, "init", "-q", "-b", "main", work); err != nil {
			return err
		}
		var text bytes.Buffer
		for i := 0; text.Len() < 50<<20; i++ {
			fmt.Fprintf(&text, "line %08d of the large text file, as a log or a generated file has\n", i)
		}
		bin := make([]byte, 100<<20)
		rand.Read(bin)
		if err := os.WriteFile(filepath.Join(work, "big.txt"), text.Bytes(), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(work, "big.bin"), bin, 0o644); err != nil {
			return err
		}
		if _, err := git(work, nil, "add", "."); err != nil {
			return err
		}
		if _, err := git(work, nil, "commit", "-q", "-m", "big"); err != nil {
			return err
		}
		// Every 500th line changes; the binary's first megabyte does.
		lines := bytes.SplitAfter(text.Bytes(), []byte("\n"))
		for i := 0; i < len(lines); i += 500 {
			lines[i] = []byte("changed " + string(lines[i]))
		}
		if err := os.WriteFile(filepath.Join(work, "big.txt"), bytes.Join(lines, nil), 0o644); err != nil {
			return err
		}
		rand.Read(bin[:1<<20])
		return os.WriteFile(filepath.Join(work, "big.bin"), bin, 0o644)
	})
	if err != nil {
		return err
	}
	s, p, err := open(repoDir(dir))
	if err != nil {
		return err
	}
	defer p.close()
	if err := p.until("the status", time.Minute, func() bool { return s.State.Status != nil }); err != nil {
		return err
	}
	for _, f := range []struct{ name, path string }{{"50 MB text", "big.txt"}, {"100 MB binary", "big.bin"}} {
		i := indexOf(s, f.path)
		if i < 0 {
			return fmt.Errorf("%s is not changed", f.path)
		}
		st := s.State.Status
		ids := st.OIDs(i)
		modes := st.Modes(i)
		sides := session.Sides{Old: ids[1], WorkTreePath: f.path, Modes: [2]uint32{modes[1], modes[2]}, Max: 512 << 20}
		t0 := time.Now()
		var res *session.PatchResult
		s.Patch(diff.PatchRequest{Source: diff.Unstaged, Paths: []string{f.path}, Context: -1, MaxBytes: 512 << 20}, sides, func(r session.PatchResult) { res = &r })
		if err := p.until("the patch", 5*time.Minute, func() bool { return res != nil }); err != nil {
			return err
		}
		if res.Err != nil {
			return res.Err
		}
		m.time(f.name+": git and reading", time.Since(t0))
		if res.Parsed == nil || len(res.Parsed.Files) == 0 || res.Parsed.Files[0].Binary {
			m.fact(f.name, "binary")
			continue
		}
		m.fact(f.name+" patch", mib(int64(len(res.Parsed.Text))))
		t1 := time.Now()
		fps, err := editor.ParsePatch(string(res.Parsed.Text))
		if err != nil || len(fps) == 0 {
			return fmt.Errorf("the viewer cannot take the patch: %v", err)
		}
		d := &editor.DiffState{}
		if err := d.SetPatchFiles(fps[0], string(res.Old), string(res.New)); err != nil {
			return err
		}
		m.time(f.name+": viewer built", time.Since(t1))
	}
	return nil
}

func indexOf(s *session.Session, path string) int {
	st := s.State.Status
	for i := range st.Len() {
		if string(st.Path(i)) == path {
			return i
		}
	}
	return -1
}

// randomHex is n random bytes in hex.
func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// touchOne times the status showing one file saved in an open repository
// of 20,000 files, through the watcher, five times a run.
func touchOne(m *meter) error {
	dir, err := made("touch-20k", func(dir string) error { return fastImport(dir, 1, 20000, 20000) })
	if err != nil {
		return err
	}
	work := repoDir(dir)
	if _, err := git(work, nil, "reset", "-q", "--hard"); err != nil {
		return err
	}
	s, p, err := open(work)
	if err != nil {
		return err
	}
	defer p.close()
	clean := func() bool { return s.State.Status != nil && s.State.Status.Len() == 0 }
	if err := p.until("the status", time.Minute, clean); err != nil {
		return err
	}
	f := filepath.Join(work, "dir042", "file00042.txt")
	orig, err := os.ReadFile(f)
	if err != nil {
		return err
	}
	for i := range 5 {
		// Let the watcher's last events pass.
		time.Sleep(300 * time.Millisecond)
		t0 := time.Now()
		if err := os.WriteFile(f, []byte(fmt.Sprintf("touched %d\n", i)), 0o644); err != nil {
			return err
		}
		if err := p.until("the change", time.Minute, func() bool { return s.State.Status != nil && s.State.Status.Len() == 1 }); err != nil {
			return err
		}
		m.time("saved to status", time.Since(t0))
		if err := os.WriteFile(f, orig, 0o644); err != nil {
			return err
		}
		if err := p.until("clean again", time.Minute, clean); err != nil {
			return err
		}
	}
	m.fact("files", 20000)
	return nil
}
