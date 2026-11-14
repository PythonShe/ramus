package write

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"ramus/internal/git/run"
)

// PushTimeout bounds a push or fetch over a slow link.
const PushTimeout = time.Hour

// PushOptions describe a push of the branch HEAD is on.
type PushOptions struct {
	// Remote, when set, pushes HEAD there and makes it the upstream;
	// otherwise the branch goes to its upstream as git push does.
	Remote string
	// ForceWithLease overwrites the remote branch, but only if it is
	// still where this repository last saw it, and that was taken in.
	ForceWithLease bool
}

// Push sends the current branch to its remote.
func Push(w Where, opt PushOptions) Op {
	args := []string{"push", "--progress"}
	name := "Push"
	if opt.ForceWithLease {
		args = append(args, "--force-with-lease", "--force-if-includes")
		name = "Force push"
	}
	if opt.Remote != "" {
		args = append(args, "--set-upstream", "--", opt.Remote, "HEAD")
		name += " to " + opt.Remote
	}
	return Op{Name: name, Cmd: run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: args, Timeout: PushTimeout}}
}

// Fetch fetches from remote, or every remote when it is empty, pruning
// remote branches gone from it when prune is set.
func Fetch(w Where, remote string, prune bool) (Op, error) {
	args := []string{"fetch", "--progress"}
	if prune {
		args = append(args, "--prune")
	}
	name := "Fetch all remotes"
	if remote == "" {
		args = append(args, "--all")
	} else {
		if err := checkName("remote", remote); err != nil {
			return Op{}, err
		}
		args = append(args, "--end-of-options", remote)
		name = "Fetch " + remote
	}
	return Op{Name: name, Cmd: run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: args, Timeout: PushTimeout}}, nil
}

// PullMode is how a pull takes in the upstream's commits.
type PullMode string

const (
	PullMerge  PullMode = "--no-rebase"
	PullRebase PullMode = "--rebase"
	PullFFOnly PullMode = "--ff-only"
)

// Pull fetches the upstream of the branch HEAD is on and takes it in, as
// mode says; the branch is backed up first. A rebase moves the branches
// in what it replays, as Rebase does; branches are those it may move.
func Pull(w Where, mode PullMode, branches []string, seen Seen) (Op, error) {
	switch mode {
	case PullMerge, PullRebase, PullFFOnly:
	default:
		return Op{}, errors.New("write: unknown pull mode " + string(mode))
	}
	if seen.Branch == "" {
		return Op{}, errors.New("write: pull needs a branch checked out")
	}
	return Op{Name: "Pull", Undo: MovesHead, Expect: seen.expect(), Backup: []string{seen.Branch}, Refs: branches,
		Cmd: run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: []string{"pull", "--progress", "--no-edit", string(mode)},
			Config: []string{updateRefs}, Timeout: PushTimeout}}, nil
}

// TagKind is what a tag is.
type TagKind uint8

const (
	// Lightweight is a ref to the commit and nothing more.
	Lightweight TagKind = iota
	// Annotated is a tag object with a message, a tagger and a date.
	Annotated
	// Signed is an annotated tag signed as git's config says.
	Signed
)

// CreateTag tags commit (hex) as name; an annotated or signed tag takes
// message, which a lightweight one has none of.
func CreateTag(w Where, name, commit string, kind TagKind, message string) (Op, error) {
	name = strings.TrimPrefix(name, "refs/tags/")
	if err := checkName("tag", name); err != nil {
		return Op{}, err
	}
	ref := "refs/tags/" + name
	op := Op{Name: "Create tag " + name, Undo: RefsOnly, Refs: []string{ref}, Expect: &Expect{AnyHead: true, Refs: map[string]string{ref: ""}}}
	args := []string{"tag"}
	switch kind {
	case Annotated, Signed:
		if strings.TrimSpace(message) == "" {
			return Op{}, errors.New("write: an annotated tag needs a message")
		}
		flag := "-a"
		if kind == Signed {
			flag = "-s"
		}
		args = append(args, flag, "-F", "-")
		op.Input = []byte(message)
	}
	op.Cmd = run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: append(args, "--end-of-options", name, commit), Timeout: HookTimeout}
	return op, nil
}

// DeleteTag deletes a tag at object (hex), which the user saw it at; it
// is backed up first, and undo puts it back.
func DeleteTag(w Where, name, object string) Op {
	name = strings.TrimPrefix(name, "refs/tags/")
	ref := "refs/tags/" + name
	return Op{Name: "Delete tag " + name + " at " + short(object), Undo: RefsOnly, Refs: []string{ref}, Backup: []string{ref},
		Expect: &Expect{AnyHead: true, Refs: map[string]string{ref: object}},
		Cmd:    run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: []string{"tag", "-d", "--end-of-options", name}}}
}

// PushTag sends a tag to remote.
func PushTag(w Where, remote, name string) (Op, error) {
	if err := checkName("remote", remote); err != nil {
		return Op{}, err
	}
	name = strings.TrimPrefix(name, "refs/tags/")
	return Op{Name: "Push tag " + name + " to " + remote,
		Cmd: run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: []string{"push", "--progress", "--", remote, "refs/tags/" + name}, Timeout: PushTimeout}}, nil
}

// CheckRemoteURL refuses a URL git would read as an option, one with
// control characters, which hide what it is, and the ext:: and fd::
// transports, which run commands.
func CheckRemoteURL(url string) error {
	switch {
	case strings.TrimSpace(url) == "":
		return errors.New("write: the URL is empty")
	case strings.HasPrefix(url, "-"):
		return fmt.Errorf("write: %q starts with -", url)
	case strings.ContainsFunc(url, func(r rune) bool { return r < 0x20 || r == 0x7f || r >= 0x80 && r < 0xa0 }):
		return errors.New("write: the URL has control characters")
	}
	lower := strings.ToLower(url)
	if strings.HasPrefix(lower, "ext::") || strings.HasPrefix(lower, "fd::") {
		return fmt.Errorf("write: %q runs a command; Ramus does not add such remotes", url)
	}
	return nil
}

// AddRemote adds a remote; it changes config, which undo does not.
func AddRemote(w Where, name, url string) (Op, error) {
	if err := checkName("remote", name); err != nil {
		return Op{}, err
	}
	if err := CheckRemoteURL(url); err != nil {
		return Op{}, err
	}
	return Op{Name: "Add remote " + name, Cmd: run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: []string{"remote", "add", "--", name, url}}}, nil
}

// RemoveRemote removes a remote and its remote branches.
func RemoveRemote(w Where, name string) (Op, error) {
	if err := checkName("remote", name); err != nil {
		return Op{}, err
	}
	return Op{Name: "Remove remote " + name, Cmd: run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: []string{"remote", "remove", "--", name}}}, nil
}

// RenameRemote renames a remote, its remote branches and the upstreams
// that name it.
func RenameRemote(w Where, from, to string) (Op, error) {
	if err := checkName("remote", to); err != nil {
		return Op{}, err
	}
	return Op{Name: "Rename remote " + from + " to " + to, Cmd: run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: []string{"remote", "rename", "--", from, to}}}, nil
}

// SetRemoteURL changes where a remote is.
func SetRemoteURL(w Where, name, url string) (Op, error) {
	if err := checkName("remote", name); err != nil {
		return Op{}, err
	}
	if err := CheckRemoteURL(url); err != nil {
		return Op{}, err
	}
	return Op{Name: "Change the URL of " + name, Cmd: run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: []string{"remote", "set-url", "--", name, url}}}, nil
}

// StackBranch is one branch of a stack pushed: its local name and the
// branch it goes to on the remote, both full ref names.
type StackBranch struct {
	Local, Remote string
}

// PushStack pushes the branches of a stack to remote in one push, each
// overwriting its remote branch only if that is still where this
// repository last saw it (a stack is rebased as a whole), and makes each
// pushed branch track it.
func PushStack(w Where, remote string, branches []StackBranch) (Op, error) {
	if err := checkName("remote", remote); err != nil {
		return Op{}, err
	}
	if len(branches) == 0 {
		return Op{}, errors.New("write: a stack of no branches")
	}
	args := []string{"push", "--progress", "--force-with-lease", "--force-if-includes", "--set-upstream", "--", remote}
	for _, b := range branches {
		if !strings.HasPrefix(b.Local, "refs/heads/") || !strings.HasPrefix(b.Remote, "refs/heads/") {
			return Op{}, fmt.Errorf("write: %q to %q is not a branch to a branch", b.Local, b.Remote)
		}
		args = append(args, b.Local+":"+b.Remote)
	}
	name := fmt.Sprintf("Push stack of %d branches to %s", len(branches), remote)
	return Op{Name: name, Cmd: run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: args, Timeout: PushTimeout}}, nil
}
