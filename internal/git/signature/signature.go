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
	// Reason is git's word on why a signature cannot be checked, as
	// "gpg.ssh.allowedSignersFile needs to be configured", "" when it
	// gave none.
	Reason string
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

// Commit asks git about the signature of commit (hex).
func Commit(ctx context.Context, r *run.Runner, gitDir, commit string) (Status, error) {
	res, err := r.Run(ctx, &run.Cmd{
		GitDir: gitDir, Read: true, Untranslated: true, MaxOutput: maxStatusText,
		Args: []string{"log", "-1", "--no-walk", "--format=%G?%x00%GS%x00%GK", commit, "--"},
	})
	if err != nil {
		return Status{}, err
	}
	return parse(res.Stdout, res.Stderr)
}

// parse reads %G?, %GS and %GK, and what git said besides. git gives N
// for a signature it could not check, as one made with an SSH key while
// no allowed signers are set, saying why: that is a signature, which
// cannot be checked.
func parse(out []byte, stderr string) (Status, error) {
	f := strings.Split(strings.TrimRight(string(out), "\n"), "\x00")
	if len(f) != 3 || len(f[0]) != 1 {
		return Status{}, errors.New("signature: git said " + strings.TrimSpace(string(out)))
	}
	s := Status{Code: Code(f[0][0]), Signer: f[1], Key: f[2]}
	if s.Code == None || s.Code == CannotCheck {
		if s.Reason = reason(stderr); s.Reason != "" {
			s.Code = CannotCheck
		}
	}
	return s, nil
}

// reason is git's errors in stderr, without their "error: ".
func reason(stderr string) string {
	var out []string
	for _, line := range strings.Split(stderr, "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "[GNUPG:]") {
			out = append(out, strings.TrimPrefix(line, "error: "))
		}
	}
	return strings.Join(out, " ")
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
	if s.Code == CannotCheck {
		s.Reason = reason(stderr)
	}
	return s
}
