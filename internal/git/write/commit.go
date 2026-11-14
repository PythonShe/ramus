package write

import (
	"errors"
	"time"

	"ramus/internal/git/message"
	"ramus/internal/git/run"
	"ramus/internal/git/textenc"
)

// CommitOptions describe a commit.
type CommitOptions struct {
	Message string
	// Amend replaces HEAD's commit; Seen is the commit the user saw
	// there, and the amend refuses if HEAD moved since.
	Amend bool
	Seen  string
	// Branch is the branch HEAD names ("refs/heads/main"), "" when
	// detached: its value is backed up before an amend.
	Branch    string
	SignOff   bool
	NoVerify  bool
	CoAuthors []string
	// Encoding is i18n.commitEncoding: git labels the commit with it and
	// takes the message as already in it, so it is encoded here.
	Encoding string
	// Sign signs the commit, or not, whatever commit.gpgSign says. git
	// signs it with the key and program its config names: GPG, SSH or
	// X.509.
	Sign SignMode
}

// SignMode is whether a commit is signed.
type SignMode int

const (
	// SignAsConfigured leaves it to commit.gpgSign.
	SignAsConfigured SignMode = iota
	SignYes
	SignNo
)

// Commit records the index as a new commit, the message read from
// standard input, so nothing in it is taken as an option. Undo moves the
// branch back and keeps the index, as git reset --soft does.
func Commit(w Where, opt CommitOptions) (Op, error) {
	if err := message.Check(opt.Message); err != nil {
		return Op{}, err
	}
	args := []string{"commit", "-F", "-"}
	name := "Commit"
	op := Op{Undo: RefsOnly}
	if opt.Amend {
		args = append(args, "--amend")
		name = "Amend commit"
		if opt.Seen == "" {
			return Op{}, errors.New("write: there is no commit to amend")
		}
		op.Expect = &Expect{Branch: opt.Branch, Head: opt.Seen}
		if opt.Branch != "" {
			op.Backup = []string{opt.Branch}
		}
	}
	if opt.SignOff {
		args = append(args, "--signoff")
	}
	switch opt.Sign {
	case SignYes:
		args = append(args, "--gpg-sign")
	case SignNo:
		args = append(args, "--no-gpg-sign")
	}
	if opt.NoVerify {
		args = append(args, "--no-verify")
		name += " without hooks"
	}
	for _, p := range opt.CoAuthors {
		if err := message.CheckPerson(p); err != nil {
			return Op{}, err
		}
		trailer, err := textenc.Encode("Co-authored-by: "+p, opt.Encoding)
		if err != nil {
			return Op{}, err
		}
		args = append(args, "--trailer", string(trailer))
	}
	msg, err := textenc.Encode(opt.Message, opt.Encoding)
	if err != nil {
		return Op{}, err
	}
	op.Name, op.Cmd, op.Input = name, run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: args, Timeout: HookTimeout}, msg
	return op, nil
}

// HookTimeout bounds a write that runs the user's hooks, which may run
// a test suite.
const HookTimeout = 30 * time.Minute
