package write

import (
	"context"
	"errors"
	"strings"

	"ramus/internal/git/refs"
	"ramus/internal/git/run"
)

// Identity is who the user's global git config says commits are by,
// user.name and user.email, and the branch git init starts a repository
// on, init.defaultBranch; "" for one unset.
type Identity struct {
	Name, Email, DefaultBranch string
}

// identityKeys are the config keys of Identity's fields, in their order.
var identityKeys = [3]string{"user.name", "user.email", "init.defaultBranch"}

func (id Identity) values() [3]string {
	return [3]string{strings.TrimSpace(id.Name), strings.TrimSpace(id.Email), strings.TrimSpace(id.DefaultBranch)}
}

// ErrBranchName is a default branch git would refuse as a branch's name.
var ErrBranchName = errors.New("write: not a name git takes for a branch")

// ReadIdentity reads Identity from the user's global git config, with
// the files it includes: a config named, as --global names it, includes
// none unless asked, and an identity kept in an included file, as
// ~/.gitconfig.local, is the one git commits with.
func ReadIdentity(ctx context.Context, r *run.Runner) (Identity, error) {
	var v [3]string
	for i, key := range identityKeys {
		s, err := globalGet(ctx, r, key, "--includes")
		if err != nil {
			return Identity{}, err
		}
		v[i] = s
	}
	return Identity{Name: v[0], Email: v[1], DefaultBranch: v[2]}, nil
}

// SetIdentity writes to the user's global git config each field of now
// that differs from was, as read; a field left empty is unset, as git
// then guesses it, or falls back to its own default branch.
func SetIdentity(ctx context.Context, r *run.Runner, was, now Identity) error {
	if b := strings.TrimSpace(now.DefaultBranch); b != "" && (strings.HasPrefix(b, "-") || !refs.ValidName("refs/heads/"+b)) {
		return ErrBranchName
	}
	old, next := was.values(), now.values()
	for i, key := range identityKeys {
		if old[i] == next[i] {
			continue
		}
		var err error
		if next[i] == "" {
			// Exit code 5 is a key that is not set.
			if err = global(ctx, r, "config", "--global", "--unset-all", key); exitCode(err) == 5 {
				err = nil
			}
		} else {
			// The value is never read as an option, whatever it starts with.
			err = global(ctx, r, "config", "--global", "--replace-all", "--", key, next[i])
		}
		if err != nil {
			return err
		}
	}
	return nil
}
