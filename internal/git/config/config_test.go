package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

func TestRead(t *testing.T) {
	r := gittest.Init(t)
	inc := filepath.Join(t.TempDir(), "extra.cfg")
	os.WriteFile(inc, []byte("[core]\n\tbigFileThreshold = 2m\n[Remote \"Up\"]\n\tpromisor = true\n"), 0o644)
	r.Git("config", "include.path", inc)
	r.Git("config", "user.name", "Rä Mus")
	r.Git("config", "--add", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	r.Git("config", "--add", "remote.origin.fetch", "+refs/tags/*:refs/tags/*")
	f, _ := os.OpenFile(filepath.Join(r.GitDir(), "config"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("[feature]\n\tflag\n\tmultiline = \"a\\nb\"\n")
	f.Close()
	c, err := Read(context.Background(), testrun.Runner(t), r.GitDir())
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := c.Get("user.name"); v != "Rä Mus" {
		t.Errorf("user.name %q", v)
	}
	if e, _ := c.Lookup("core.bigfilethreshold"); c.Int("core.bigFileThreshold", 0) != 2<<20 || e.Origin != "file:"+inc || e.Scope != Local {
		t.Errorf("included value %+v", e)
	}
	if len(c.All("remote.origin.fetch")) != 2 {
		t.Errorf("fetch %q", c.All("remote.origin.fetch"))
	}
	if !c.Bool("feature.flag", false) {
		t.Error("key without = is not true")
	}
	if v, _ := c.Get("feature.multiline"); v != "a\nb" {
		t.Errorf("multiline %q", v)
	}
	if subs := c.Subsections("remote", "promisor"); len(subs) != 1 || subs[0] != "Up" {
		t.Errorf("subsections %q", subs)
	}
	if c.Bool("core.bare", true) {
		t.Error("core.bare")
	}
}

func TestParseErrors(t *testing.T) {
	if _, err := Parse([]byte("local\x00file:x\x00key")); err == nil {
		t.Fatal("parsed a cut entry")
	}
	c, err := Parse(nil)
	if err != nil || len(c.Entries()) != 0 {
		t.Fatal("empty")
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte("local\x00file:.git/config\x00core.bare\nfalse\x00"))
	f.Fuzz(func(t *testing.T, b []byte) {
		if c, err := Parse(b); err == nil {
			c.Bool("a.b", false)
			c.Int("a.b", 0)
			c.Subsections("a", "b")
		}
	})
}

// busy knows Git for Windows' message for a config file being replaced.
func TestBusy(t *testing.T) {
	msg := "warning: unable to access '.git/config': Permission denied\nfatal: unknown error occurred while reading the configuration files"
	if !busy(msg) || busy("fatal: bad config line 1 in file .git/config") {
		t.Fatal("busy")
	}
}
