package ui

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"

	"ramus/internal/askpass"
	"ramus/internal/git/gittest"
)

// git asks for a user name and a password in the window, the host it
// goes to named; a question whose helper is gone leaves the window.
func TestAskpassDialog(t *testing.T) {
	h := newHarness(t)
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
	cmd.Env, cmd.Dir = env, t.TempDir()
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
