package askpass

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/gittest"
)

// The test binary is the helper when git starts it as one.
func TestMain(m *testing.M) {
	if IsHelper() {
		os.Exit(Helper(os.Args[1:], os.Stdout))
	}
	os.Exit(m.Run())
}

func TestKindAndHost(t *testing.T) {
	for _, c := range []struct {
		prompt, mode string
		kind         Kind
		host         string
	}{
		{"Username for 'https://github.com': ", "", Text, "github.com"},
		{"Password for 'https://me@github.com@evil.example': ", "", Secret, "evil.example"},
		{"Enter passphrase for key '/home/me/.ssh/id_ed25519': ", "", Secret, ""},
		{"me@build.example's password: ", "", Secret, "build.example"},
		{"The authenticity of host 'git.example (10.0.0.1)' can't be established.\nED25519 key fingerprint is SHA256:x.\nAre you sure you want to continue connecting (yes/no/[fingerprint])? ", "", HostKey, "git.example"},
		{"Allow use of key /k?", "confirm", Confirm, ""},
		{"Confirm user presence for key ED25519-SK SHA256:x", "none", Notice, ""},
	} {
		q := Question{Prompt: c.prompt, Kind: kindOf(c.prompt, c.mode)}
		if q.Kind != c.kind || q.Host() != c.host {
			t.Errorf("%q: kind %d host %q, want %d %q", c.prompt, q.Kind, q.Host(), c.kind, c.host)
		}
	}
}

// serve answers each question with reply, after telling asked.
func serve(t *testing.T, reply func(*Ask)) *Server {
	t.Helper()
	s, err := Listen(reply)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// git asks for a user name and a password through the helper; the
// answers are what git hands on, the oracle.
func TestGitAsks(t *testing.T) {
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var asked []Question
	s := serve(t, func(a *Ask) {
		asked = append(asked, a.Question) // one at a time: git asks in turn
		if a.Kind == Text {
			a.Answer("me")
		} else {
			a.Answer("s3cret")
		}
	})
	var env []string
	for _, kv := range gittest.Env() {
		if !strings.HasPrefix(kv, "SSH_ASKPASS") && !strings.HasPrefix(kv, "DISPLAY=") {
			env = append(env, kv)
		}
	}
	cmd := exec.Command("git", "-c", "credential.helper=", "credential", "fill")
	cmd.Env = append(env, s.Env(program)...)
	cmd.Dir = t.TempDir()
	cmd.Stdin = strings.NewReader("protocol=https\nhost=example.invalid\n\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("username=me\n")) || !bytes.Contains(out, []byte("password=s3cret\n")) {
		t.Fatalf("git got\n%s", out)
	}
	if len(asked) != 2 || asked[0].Kind != Text || asked[1].Kind != Secret || asked[1].Host() != "example.invalid" || asked[0].Dir == "" {
		t.Fatalf("asked %+v", asked)
	}

	// Declined: git has no password and says so.
	s2 := serve(t, func(a *Ask) { a.Cancel() })
	cmd = exec.Command("git", "-c", "credential.helper=", "credential", "fill")
	cmd.Env = append(env, s2.Env(program)...)
	cmd.Dir = t.TempDir()
	cmd.Stdin = strings.NewReader("protocol=https\nhost=example.invalid\n\n")
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("declined, yet\n%s", out)
	}
}

// A helper without the token is not asked about; one that goes away
// before an answer is gone.
func TestHelperChecks(t *testing.T) {
	asked := make(chan *Ask, 1)
	s := serve(t, func(a *Ask) { asked <- a })
	env := s.Env("unused")
	for _, kv := range env[2:] {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	t.Setenv(envToken, "wrong")
	if code := Helper([]string{"Password: "}, new(bytes.Buffer)); code != 1 {
		t.Fatalf("wrong token: exit %d", code)
	}
	select {
	case <-asked:
		t.Fatal("asked with a wrong token")
	default:
	}

	t.Setenv(envToken, s.token)
	var out bytes.Buffer
	done := make(chan int)
	go func() { done <- Helper([]string{"Confirm user presence for key"}, &out) }()
	a := <-asked
	a.Answer("x")
	if code := <-done; code != 0 || out.String() != "x\n" {
		t.Fatalf("exit %d, printed %q", code, out.String())
	}
	select {
	case <-a.Gone():
	case <-time.After(5 * time.Second):
		t.Fatal("an answered question is not gone")
	}
	s.mu.Lock()
	open := s.open
	s.mu.Unlock()
	if open != 0 {
		t.Fatalf("%d open", open)
	}
}
