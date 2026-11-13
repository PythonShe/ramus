package userenv

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func fakeShell(t *testing.T, body string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("unix shells")
	}
	p := filepath.Join(t.TempDir(), "sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", p)
}

func TestResolve(t *testing.T) {
	// A shell whose startup files print noise around the env output.
	fakeShell(t, `echo "welcome banner"
printf '\037RAMUS-ENV\037'
printf 'PATH=/opt/tools/bin:/usr/bin\0SSH_AUTH_SOCK=/tmp/agent.sock\0SECRET=nope\0'
printf '\037RAMUS-ENV\037'
echo "bye"
`)
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("SECRET", "kept")
	r := Resolve(context.Background())
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	if got := Lookup(r.Env, "PATH"); got != "/opt/tools/bin:/usr/bin" {
		t.Errorf("PATH %q", got)
	}
	if got := Lookup(r.Env, "SSH_AUTH_SOCK"); got != "/tmp/agent.sock" {
		t.Errorf("SSH_AUTH_SOCK %q", got)
	}
	if got := Lookup(r.Env, "SECRET"); got != "kept" {
		t.Errorf("a variable outside Keys was replaced: %q", got)
	}
	n := 0
	for _, kv := range r.Env {
		if strings.HasPrefix(kv, "PATH=") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d PATH entries", n)
	}
}

func TestResolveHangingShell(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the timeout")
	}
	fakeShell(t, "sleep 60\n")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	r := Resolve(ctx)
	if r.Err == nil {
		t.Fatal("no error from a hanging shell")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("waited %v", time.Since(start))
	}
	if Lookup(r.Env, "PATH") != os.Getenv("PATH") {
		t.Fatal("fallback environment lost PATH")
	}
}

func TestResolveBrokenShell(t *testing.T) {
	fakeShell(t, "echo oops >&2; exit 3\n")
	r := Resolve(context.Background())
	if r.Err == nil || Lookup(r.Env, "PATH") != os.Getenv("PATH") {
		t.Fatalf("broken shell: %v", r.Err)
	}
}

// The real login shell, when there is one.
func TestResolveReal(t *testing.T) {
	if os.Getenv("SHELL") == "" || runtime.GOOS == "windows" {
		t.Skip("no login shell")
	}
	r := Resolve(context.Background())
	if r.Err != nil {
		t.Skipf("login shell unusable here: %v", r.Err)
	}
	if Lookup(r.Env, "PATH") == "" {
		t.Fatal("no PATH")
	}
}
