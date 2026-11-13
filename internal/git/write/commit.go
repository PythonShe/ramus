package write

import (
	"errors"
	"time"

	"ramus/internal/git/message"
	"ramus/internal/git/run"
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
}

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
	if opt.NoVerify {
		args = append(args, "--no-verify")
		name += " without hooks"
	}
	for _, p := range opt.CoAuthors {
		if err := message.CheckPerson(p); err != nil {
			return Op{}, err
		}
		args = append(args, "--trailer", "Co-authored-by: "+p)
	}
	op.Name, op.Cmd, op.Input = name, run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: args, Timeout: HookTimeout}, []byte(opt.Message)
	return op, nil
}

// HookTimeout bounds a write that runs the user's hooks, which may run
// a test suite.
const HookTimeout = 30 * time.Minute
