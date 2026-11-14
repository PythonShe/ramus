package signature

import (
	"context"
	"os"
	"strings"
	"testing"

	"ramus/internal/git/gitexe"
	"ramus/internal/git/gittest"
	"ramus/internal/git/run"
)

func runner(t *testing.T) *run.Runner {
	t.Helper()
	g, err := gitexe.Find(context.Background(), os.Getenv("PATH"))
	if err != nil {
		t.Skip(err)
	}
	return run.New(g, gittest.Env(), 2)
}

// Commits and tags signed with an SSH key, or not, as git verifies them.
func TestVerify(t *testing.T) {
	r := gittest.Init(t)
	allowed := gittest.SSHSigning(t, r)
	run := runner(t)
	ctx := context.Background()
	r.Write("a", "1")
	r.Commit("unsigned")
	unsigned := strings.TrimSpace(r.Git("rev-parse", "HEAD"))
	r.Write("a", "2")
	r.Git("add", "a")
	r.Git("commit", "-q", "-S", "-m", "signed")
	signed := strings.TrimSpace(r.Git("rev-parse", "HEAD"))
	r.Git("tag", "-s", "-m", "signed tag", "v1")
	r.Git("tag", "-a", "-m", "plain tag", "v0", unsigned)

	commit := func(oid string) Status {
		t.Helper()
		s, err := Commit(ctx, run, r.GitDir(), oid)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	tag := func(name string) Status {
		t.Helper()
		s, err := Tag(ctx, run, r.GitDir(), name)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s := commit(signed); !s.Verified() || s.Signer != "test@example.com" || !strings.HasPrefix(s.Key, "SHA256:") {
		t.Fatalf("signed commit %+v", s)
	}
	if s := commit(unsigned); s.Code != None {
		t.Fatalf("unsigned commit %+v", s)
	}
	if s := tag("refs/tags/v1"); !s.Verified() || s.Signer != "test@example.com" || !strings.HasPrefix(s.Key, "SHA256:") {
		t.Fatalf("signed tag %+v", s)
	}
	if s := tag("refs/tags/v0"); s.Code != None {
		t.Fatalf("plain tag %+v", s)
	}

	// A signer no longer allowed: the signature cannot be checked.
	if err := os.WriteFile(allowed, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if s := commit(signed); s.Verified() {
		t.Fatalf("commit by a signer not allowed %+v", s)
	}
	if s := tag("refs/tags/v1"); s.Verified() {
		t.Fatalf("tag by a signer not allowed %+v", s)
	}
}

func TestTagStatusGPG(t *testing.T) {
	gpg := "[GNUPG:] NEWSIG\n[GNUPG:] GOODSIG 0123456789ABCDEF Ada <ada@example.com>\n[GNUPG:] TRUST_UNDEFINED 0 pgp\n"
	if s := tagStatus(true, gpg); s.Code != Untrusted || s.Key != "0123456789ABCDEF" || s.Signer != "Ada <ada@example.com>" {
		t.Fatalf("%+v", s)
	}
	if s := tagStatus(false, "[GNUPG:] BADSIG 0123456789ABCDEF Ada\n"); s.Code != Bad {
		t.Fatalf("%+v", s)
	}
	if s := tagStatus(false, "[GNUPG:] ERRSIG 0123456789ABCDEF 1 8 00 0 9\n[GNUPG:] NO_PUBKEY 0123456789ABCDEF\n"); s.Code != CannotCheck {
		t.Fatalf("%+v", s)
	}
}
