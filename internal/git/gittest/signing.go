package gittest

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// SSHSigning sets r up to sign with a new SSH key, made by ssh-keygen,
// and returns the allowed-signers file that verifies it.
func SSHSigning(t testing.TB, r *Repo) string {
	t.Helper()
	keygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("no ssh-keygen:", err)
	}
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	if out, err := exec.Command(keygen, "-q", "-t", "ed25519", "-N", "", "-C", "test", "-f", key).CombinedOutput(); err != nil {
		t.Skipf("ssh-keygen: %v\n%s", err, out)
	}
	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	allowed := filepath.Join(dir, "allowed")
	if err := os.WriteFile(allowed, []byte(`test@example.com namespaces="git" `+string(pub)), 0o600); err != nil {
		t.Fatal(err)
	}
	r.Git("config", "gpg.format", "ssh")
	r.Git("config", "user.signingKey", filepath.ToSlash(key))
	r.Git("config", "gpg.ssh.allowedSignersFile", filepath.ToSlash(allowed))
	return allowed
}
