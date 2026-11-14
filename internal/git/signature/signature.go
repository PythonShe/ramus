// Package signature asks git whether a commit's or a tag's signature is
// good. git checks it with the program its config names for the
// signature's kind (gpg, ssh-keygen or gpgsm), so the verdict is the one
// the terminal gives.
package signature

import (
	"context"
	"errors"
	"strings"

	"ramus/internal/git/run"
)

// Code is git's verdict, as its %G? placeholder gives it.
type Code byte

const (
	Good          Code = 'G'
	Bad           Code = 'B'
	Untrusted     Code = 'U' // good, from a key not fully trusted
	Expired       Code = 'X' // good, the signature expired
	KeyExpired    Code = 'Y' // good, made by a key since expired
	Revoked       Code = 'R' // made by a revoked key
	CannotCheck   Code = 'E' // the key is missing, or no program checks it
	None          Code = 'N'
	maxStatusText      = 4 << 10
)

// Status is the verdict on one signature.
type Status struct {
	Code Code
	// Signer is who the key belongs to, as the checking program names
	// them; Key the key's ID or fingerprint.
	Signer, Key string
}

// Verified reports a good signature from a key git trusts: for SSH, one
// the allowed signers name. A good signature from any other key proves
// only that someone holds that key.
func (s Status) Verified() bool { return s.Code == Good }

// good reports a signature that checks, whatever the trust in its key.
func (s Status) good() bool {
	switch s.Code {
	case Good, Untrusted, Expired, KeyExpired:
		return true
	}
	return false
}

// Text says what the verdict is.
func (s Status) Text() string {
	switch s.Code {
	case Good:
		return "Good signature"
	case Untrusted:
		return "Good signature, from a key not trusted"
	case Expired:
		return "Good signature, expired"
	case KeyExpired:
		return "Good signature, from a key since expired"
	case Revoked:
		return "Signed with a revoked key"
	case Bad:
		return "Bad signature"
	case CannotCheck:
		return "Signature cannot be checked: the key or the program to check it is missing"
	}
	return "Not signed"
}

// Commit asks git about the signature of commit (hex).
func Commit(ctx context.Context, r *run.Runner, gitDir, commit string) (Status, error) {
	res, err := r.Run(ctx, &run.Cmd{
		GitDir: gitDir, Read: true, Untranslated: true, MaxOutput: maxStatusText,
		Args: []string{"log", "-1", "--no-walk", "--format=%G?%x00%GS%x00%GK", commit, "--"},
	})
	if err != nil {
		return Status{}, err
	}
	return parse(res.Stdout)
}

func parse(out []byte) (Status, error) {
	f := strings.Split(strings.TrimRight(string(out), "\n"), "\x00")
	if len(f) != 3 || len(f[0]) != 1 {
		return Status{}, errors.New("signature: git said " + strings.TrimSpace(string(out)))
	}
	return Status{Code: Code(f[0][0]), Signer: f[1], Key: f[2]}, nil
}

// Tag asks git about the signature of the tag named (a full ref name).
// git gives no %G? for a tag: its exit code says good or not, and the
// checking program's first line says who signed it.
func Tag(ctx context.Context, r *run.Runner, gitDir, name string) (Status, error) {
	res, err := r.Run(ctx, &run.Cmd{
		GitDir: gitDir, Read: true, Untranslated: true, MaxOutput: maxStatusText,
		Args: []string{"verify-tag", "--raw", "--end-of-options", name},
	})
	var e *run.Error
	if err != nil && !errors.As(err, &e) {
		return Status{}, err
	}
	stderr := res.Stderr
	if e != nil {
		stderr = e.Stderr
	}
	return tagStatus(err == nil, stderr), nil
}

// tagStatus reads gpg's status lines (--raw) or ssh-keygen's message.
func tagStatus(ok bool, stderr string) Status {
	var s Status
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "[GNUPG:] GOODSIG "), strings.HasPrefix(line, "[GNUPG:] EXPSIG "),
			strings.HasPrefix(line, "[GNUPG:] EXPKEYSIG "), strings.HasPrefix(line, "[GNUPG:] REVKEYSIG "),
			strings.HasPrefix(line, "[GNUPG:] BADSIG "):
			f := strings.SplitN(line, " ", 4)
			if len(f) >= 3 {
				s.Key = f[2]
			}
			if len(f) == 4 {
				s.Signer = f[3]
			}
			switch f[1] {
			case "GOODSIG":
				s.Code = Good
			case "EXPSIG":
				s.Code = Expired
			case "EXPKEYSIG":
				s.Code = KeyExpired
			case "REVKEYSIG":
				s.Code = Revoked
			default:
				s.Code = Bad
			}
		case strings.HasPrefix(line, "[GNUPG:] TRUST_UNDEFINED"), strings.HasPrefix(line, "[GNUPG:] TRUST_NEVER"):
			if s.Code == Good {
				s.Code = Untrusted
			}
		case strings.HasPrefix(line, "[GNUPG:] NO_PUBKEY") || strings.HasPrefix(line, "[GNUPG:] ERRSIG"):
			if s.Code == 0 {
				s.Code = CannotCheck
			}
		case strings.HasPrefix(line, `Good "git" signature for `):
			// ssh-keygen: Good "git" signature for NAME with ED25519 key SHA256:...
			rest := strings.TrimPrefix(line, `Good "git" signature for `)
			who, key, _ := strings.Cut(rest, " with ")
			s.Code, s.Signer = Good, who
			if i := strings.LastIndex(key, " "); i >= 0 {
				s.Key = key[i+1:]
			}
		}
	}
	switch {
	case s.Code == 0 && ok:
		s.Code = Good
	case s.Code == 0 && strings.Contains(stderr, "no signature found"):
		s.Code = None
	case s.Code == 0:
		s.Code = CannotCheck
	case !ok && s.good():
		// git refused what the program called good, as an ssh key not
		// in the allowed signers.
		s.Code = CannotCheck
	}
	return s
}
