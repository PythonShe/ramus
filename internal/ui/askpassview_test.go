package ui

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"ramus/internal/askpass"
	"ramus/internal/git/gittest"
)

// git asks for a user name and a password in the window, the host it
// goes to named when a remote of the repository asking is on it; a
// question whose helper is gone leaves the window.
func TestAskpassDialog(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Git("remote", "add", "origin", "https://git.example/r.git")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("open", h.shows("No local changes"))
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	s, err := askpass.Listen(func(q *askpass.Ask) { h.updates <- func() { h.app.Ask(q) } })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	var env []string
	for _, kv := range gittest.Env() {
		if !strings.HasPrefix(kv, "SSH_ASKPASS") && !strings.HasPrefix(kv, "DISPLAY=") {
			env = append(env, kv)
		}
	}
	env = append(env, s.Env(program)...)

	cmd := exec.Command("git", "-c", "credential.helper=", "credential", "fill")
	cmd.Env, cmd.Dir = env, r.Dir
	cmd.Stdin = strings.NewReader("url=https://git.example/r.git\n\n")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	h.until("the user name", h.shows("User name for git.example"))
	unnamed(h, "askpass")
	h.tt.Type("me")
	h.click("OK")
	h.until("the password", h.shows("Password for git.example"))
	h.tt.Type("s3cret")
	if h.tt.HasText("s3cret") {
		t.Fatal("the password shows")
	}
	h.click("OK")
	if err := <-done; err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if s := out.String(); !strings.Contains(s, "username=me\n") || !strings.Contains(s, "password=s3cret\n") {
		t.Fatalf("git got\n%s", s)
	}

	// An ssh server's keyboard-interactive question imitating git's, or a
	// question from a folder no repository open here holds, names no host
	// in its title: the prompt alone says it.
	for _, tc := range []struct{ dir, prompt string }{
		{r.Dir, "(git@evil.example) Password for 'https://git.example': "},
		{t.TempDir(), "Password for 'https://git.example': "},
	} {
		helper := exec.Command(program, tc.prompt)
		helper.Env, helper.Dir = env, tc.dir
		if err := helper.Start(); err != nil {
			t.Fatal(err)
		}
		h.until("the question", h.shows(strings.TrimSpace(tc.prompt)))
		h.settle()
		if !h.tt.HasText("Password") || h.tt.HasText("Password for git.example") || h.tt.HasText("Password for evil.example") {
			t.Fatalf("%q titled: %q", tc.prompt, h.tt.Texts())
		}
		h.click("Cancel")
		helper.Wait()
		h.until("the question gone", func() bool { return len(h.app.asking.list) == 0 })
	}

	// A notice to touch a key goes when ssh ends the helper.
	helper := exec.Command(program, "Confirm user presence for key ED25519-SK SHA256:x")
	helper.Env = append(env, "SSH_ASKPASS_PROMPT=none")
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	h.until("the notice", h.shows("Security key"))
	if !h.tt.HasText("Confirm user presence for key ED25519-SK SHA256:x") {
		t.Fatalf("texts %q", h.tt.Texts())
	}
	helper.Process.Kill()
	helper.Wait()
	h.until("the notice gone", func() bool { return len(h.app.asking.list) == 0 && !h.tt.HasText("Security key") })
}

// A window closing leaves no question from git unanswered: one asked in
// a repository open there is declined, its git stopping with the window;
// one asked elsewhere, as by a clone in another window, is handed on;
// and one still left when the window is gone is declined too.
func TestAskpassWindowCloses(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	h.app.Open(r.Dir)
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	s, err := askpass.Listen(func(q *askpass.Ask) { h.updates <- func() { h.app.Ask(q) } })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	elsewhere := t.TempDir()
	ended := map[string]chan error{}
	for _, dir := range []string{r.Dir, elsewhere} {
		helper := exec.Command(program, "Password for 'https://git.example': ")
		helper.Env, helper.Dir = append(gittest.Env(), s.Env(program)...), dir
		if err := helper.Start(); err != nil {
			t.Fatal(err)
		}
		ended[dir] = make(chan error, 1)
		go func() { ended[dir] <- helper.Wait() }()
	}
	h.until("both questions", func() bool { return len(h.app.asking.list) == 2 })
	waitEnd := func(dir string) {
		t.Helper()
		select {
		case <-ended[dir]:
		case <-time.After(10 * time.Second):
			t.Fatalf("the helper in %s still waits", dir)
		}
	}
	taken := h.app.TakeAsks()
	if len(taken) != 1 || len(h.app.asking.list) != 0 {
		t.Fatalf("took %d, left %d", len(taken), len(h.app.asking.list))
	}
	waitEnd(r.Dir)
	h.app.Ask(taken[0])
	h.app.CloseAll()
	waitEnd(elsewhere)
}
