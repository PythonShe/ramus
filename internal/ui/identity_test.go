package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ramus/internal/git/gitexe"
	"ramus/internal/git/gittest"
	"ramus/internal/git/run"
	"ramus/internal/git/write"
	"ramus/locales"
)

// identityHarness is a harness whose git knows no one: no name or email
// in the environment, and a global config, at the path returned, that
// tells git not to guess them.
func identityHarness(t *testing.T) (*harness, string) {
	t.Helper()
	g, err := gitexe.Find(context.Background(), os.Getenv("PATH"))
	if err != nil {
		t.Skip(err)
	}
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(cfg, []byte("[user]\n\tuseConfigOnly = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{"GIT_CONFIG_GLOBAL=" + cfg}
	for _, kv := range gittest.Env() {
		k, _, _ := strings.Cut(kv, "=")
		if k != "GIT_CONFIG_GLOBAL" && !strings.HasPrefix(k, "GIT_AUTHOR_") && !strings.HasPrefix(k, "GIT_COMMITTER_") {
			env = append(env, kv)
		}
	}
	return newHarnessWith(t, run.New(g, env, 8)), cfg
}

// A commit git refuses for want of a name and email says so in words a
// newcomer reads, and its remedy opens Settings at the name; what is
// typed there goes into git's global config as Settings close, and the
// commit then goes through.
func TestIdentityUnknown(t *testing.T) {
	h, cfg := identityHarness(t)
	r := h.repoWith()
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	r.Write("new.txt", "x")
	tb := h.app.current()
	h.until("the change", func() bool { return tb.s.State.Status != nil && tb.s.State.Status.Len() == 1 })
	r.Git("add", "new.txt")
	commit := func() {
		t.Helper()
		op, err := write.Commit(tb.s.Where(), write.CommitOptions{Message: "first", Branch: "refs/heads/main"})
		if err == nil {
			_, err = tb.s.Submit(op)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	commit()
	h.until("the refusal", h.shows("Commit failed: git does not know your name and email"))
	h.click("Set Name and Email…")
	s := &h.app.settingsUI
	h.until("the settings", func() bool { return s.open && s.identRead })
	h.tt.Frame()
	if s.focusName {
		t.Fatal("the name field was not focused")
	}
	h.tt.Type("Some One")
	s.ident.Email = "some@example.com"
	h.tt.Frame()
	h.click("Done")
	h.until("the config", func() bool {
		b, _ := os.ReadFile(cfg)
		return strings.Contains(string(b), "name = Some One") && strings.Contains(string(b), "email = some@example.com")
	})
	commit()
	h.until("the commit", func() bool {
		out, _ := r.Run(nil, "log", "-1", "--format=%an <%ae>")
		return strings.TrimSpace(out) == "Some One <some@example.com>"
	})
}

// A default branch git would refuse is said in Settings, not written.
func TestDefaultBranchRefused(t *testing.T) {
	h, cfg := identityHarness(t)
	h.tt.Frame()
	h.app.openSettings()
	s := &h.app.settingsUI
	h.until("the identity", func() bool { return s.identRead })
	s.ident.DefaultBranch = "two words"
	h.app.saveIdentity()
	h.until("the refusal", func() bool { return strings.Contains(s.err, "two words") })
	if b, _ := os.ReadFile(cfg); strings.Contains(string(b), "defaultBranch") {
		t.Fatalf("written:\n%s", b)
	}
}

// A global config git cannot read says so, the fields left off.
func TestIdentityUnreadable(t *testing.T) {
	h, cfg := identityHarness(t)
	if err := os.WriteFile(cfg, []byte("[user\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()
	h.app.openSettings()
	s := &h.app.settingsUI
	h.until("the reason", func() bool { return strings.Contains(s.err, "Could not read git's settings") })
	if s.identRead {
		t.Fatal("read")
	}
}

// Changes saved in quick succession are written one after the other,
// none failing on the config's lock; one typed with the settings open
// as the window closes is written too.
func TestIdentitySavesInTurn(t *testing.T) {
	h, cfg := identityHarness(t)
	h.tt.Frame()
	h.app.openSettings()
	s := &h.app.settingsUI
	h.until("the identity", func() bool { return s.identRead })
	s.ident.Name = "Some One"
	h.app.saveIdentity()
	s.ident.Email = "some@example.com"
	h.app.saveIdentity()
	s.ident.DefaultBranch = "trunk"
	h.app.saveIdentity()
	s.ident.Name = "Some Other"
	h.app.SaveIdentity()
	h.app.WaitIdentity()
	n := 0
	h.until("a failure told", func() bool { n++; return n > 5 })
	if s.err != "" {
		t.Fatal(s.err)
	}
	b, _ := os.ReadFile(cfg)
	for _, want := range []string{"name = Some Other", "email = some@example.com", "defaultBranch = trunk"} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("no %q in:\n%s", want, b)
		}
	}
}

// With a repository shown, Settings read the identity as git uses it
// there: an email a file the global config includes for it alone gives
// is shown, said where it is from, and a change goes to that file; a
// name the repository's own config sets is shown, said so, and a change
// goes to the global config.
func TestIdentityInRepository(t *testing.T) {
	h, cfg := identityHarness(t)
	r := h.repoWith()
	h.trust.Set(r.GitDir(), true)
	r.Git("config", "user.name", "Only Here")
	work := filepath.Join(filepath.Dir(cfg), "work")
	if err := os.WriteFile(work, []byte("[user]\n\temail = work@example.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	conf := "[user]\n\tname = Everywhere\n\temail = home@example.com\n[includeIf \"gitdir:" + filepath.ToSlash(r.Dir) + "/\"]\n\tpath = " + filepath.ToSlash(work) + "\n"
	if err := os.WriteFile(cfg, []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	h.app.openSettings()
	s := &h.app.settingsUI
	h.until("the identity", func() bool { return s.identRead })
	if s.ident.Name != "Only Here" || s.ident.Email != "work@example.com" {
		t.Fatalf("read %+v", s.ident)
	}
	h.tt.Frame()
	h.until("where the email is from", h.describes(locales.SettingsGitFromFile(homeTilde(work))))
	h.until("the name the repository keeps", h.describes(locales.SettingsGitRepositoryOwn(homeTilde(filepath.Join(r.GitDir(), "config")))))
	s.ident.Name, s.ident.Email = "Changed", "job@example.com"
	h.app.saveIdentity()
	h.app.WaitIdentity()
	if b, _ := os.ReadFile(work); !strings.Contains(string(b), "email = job@example.com") {
		t.Fatalf("the included file has:\n%s", b)
	}
	b, _ := os.ReadFile(cfg)
	if !strings.Contains(string(b), "name = Changed") || !strings.Contains(string(b), "email = home@example.com") {
		t.Fatalf("the global config has:\n%s", b)
	}
	if got := strings.TrimSpace(r.Git("config", "--local", "user.name")); got != "Only Here" {
		t.Fatalf("the repository's config has %q", got)
	}
}
