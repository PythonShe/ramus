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
	h.until("the repository", h.shows("No changes"))
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
