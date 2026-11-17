package write

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"

	"ramus/internal/git/refs"
	"ramus/internal/git/run"
)

// Identity is who git says commits are by, user.name and user.email,
// and the branch git init starts a repository on, init.defaultBranch;
// "" for one unset.
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

// IdentityFrom says where each field of an Identity is set, in the
// order of its fields.
type IdentityFrom [3]Origin

// Origin is where git takes the value of a config key from.
type Origin struct {
	// File is the file of the user's global config, or of one it
	// includes, that gives the value the global config has, and a change
	// goes to; "" for none, where a change goes to the global config.
	File string
	// Included is whether File is a file the global config includes
	// rather than the global config itself.
	Included bool
	// Files are all the files of the global config, included ones among
	// them, that set the key: clearing it clears it in each, or the value
	// of another would come through.
	Files []string
	// Kept is the repository's own config file when it sets the value
	// git uses there, over the global one. The app never writes it: a
	// change goes to File as it would without it.
	Kept string
}

// ReadIdentity reads Identity as git uses it: in the repository w names
// when it names one, so that its own config and the files the global
// config includes for it (includeIf) count, else from the user's global
// config with the files it includes. A repository whose config cannot
// be read is passed over for the global config.
func ReadIdentity(ctx context.Context, r *run.Runner, w Where) (Identity, IdentityFrom, error) {
	if w.GitDir != "" {
		cmd := run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Read: true, Args: identityArgs("--includes")}
		if id, from, err := readIdentity(ctx, r, &cmd); err == nil || ctx.Err() != nil {
			return id, from, err
		}
	}
	cmd := run.Cmd{Outside: true, WorkTree: home(), Read: true, Args: identityArgs("--global", "--includes")}
	return readIdentity(ctx, r, &cmd)
}

// identityArgs is git config listing every value of the identity's keys,
// each after its scope and the file it comes from, read with opts.
func identityArgs(opts ...string) []string {
	args := append([]string{"config", "-z", "--show-scope", "--show-origin"}, opts...)
	return append(args, "--get-regexp", `^(user\.name|user\.email|init\.defaultbranch)$`)
}

type configEntry struct{ scope, file, key, value string }

// configEntries runs cmd, a listing of identityArgs, and parses what it
// prints: nothing for exit code 1, no key set. A file named relative to
// where git ran is made absolute; one that is no file, as the command
// line, is "".
func configEntries(ctx context.Context, r *run.Runner, cmd *run.Cmd) ([]configEntry, error) {
	res, err := r.Run(ctx, cmd)
	if exitCode(err) == 1 {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []configEntry
	fields := bytes.Split(res.Stdout, []byte{0})
	for i := 0; i+2 < len(fields); i += 3 {
		key, value, _ := strings.Cut(string(fields[i+2]), "\n")
		file, isFile := strings.CutPrefix(string(fields[i+1]), "file:")
		switch {
		case !isFile:
			file = ""
		case !filepath.IsAbs(file):
			file = filepath.Join(cmd.WorkTree, file)
		}
		out = append(out, configEntry{scope: string(fields[i]), file: file, key: strings.ToLower(key), value: value})
	}
	return out, nil
}

func readIdentity(ctx context.Context, r *run.Runner, cmd *run.Cmd) (Identity, IdentityFrom, error) {
	entries, err := configEntries(ctx, r, cmd)
	if err != nil {
		return Identity{}, IdentityFrom{}, err
	}
	// The global config's own files, without what they include, tell an
	// included file from them.
	top, err := configEntries(ctx, r, &run.Cmd{Outside: true, WorkTree: home(), Read: true, Args: identityArgs("--global", "--no-includes")})
	if err != nil {
		return Identity{}, IdentityFrom{}, err
	}
	var v [3]string
	var from IdentityFrom
	for i, key := range identityKeys {
		key = strings.ToLower(key)
		o := &from[i]
		// The last value git reads is the one it uses.
		for _, e := range entries {
			if e.key != key {
				continue
			}
			v[i], o.Kept = e.value, ""
			switch e.scope {
			case "global":
				if e.file != "" {
					o.File = e.file
					if !slices.Contains(o.Files, e.file) {
						o.Files = append(o.Files, e.file)
					}
				}
			case "local", "worktree":
				o.Kept = e.file
			}
		}
		o.Included = o.File != "" && !slices.ContainsFunc(top, func(e configEntry) bool { return e.file == o.File })
	}
	return Identity{Name: v[0], Email: v[1], DefaultBranch: v[2]}, from, nil
}

// SetIdentity writes each field of now that differs from was, as read
// from where from says, and returns the identity the config has after:
// was with the fields written, those before a failure, which stops it.
// A value goes to the file of the global config that gives it, so that
// it is the one git takes, or to the global config when none does. A
// field left empty is unset in every file of the global config that
// sets it, as git then guesses it, or falls back to its own default
// branch. The repository's own config and the system's are never
// written.
func SetIdentity(ctx context.Context, r *run.Runner, was, now Identity, from IdentityFrom) (Identity, error) {
	if b := strings.TrimSpace(now.DefaultBranch); b != "" && (strings.HasPrefix(b, "-") || !refs.ValidName("refs/heads/"+b)) {
		return was, ErrBranchName
	}
	in := func(file string) []string {
		if file == "" {
			return []string{"config", "--global"}
		}
		return []string{"config", "--file", file}
	}
	old, next := was.values(), now.values()
	done := [3]*string{&was.Name, &was.Email, &was.DefaultBranch}
	for i, key := range identityKeys {
		if old[i] == next[i] {
			continue
		}
		o := from[i]
		if next[i] != "" {
			// The value is never read as an option, whatever it starts with.
			if err := global(ctx, r, append(in(o.File), "--replace-all", "--", key, next[i])...); err != nil {
				return was, err
			}
			*done[i] = next[i]
			continue
		}
		files := o.Files
		if len(files) == 0 {
			files = []string{""}
		}
		for _, f := range files {
			// Exit code 5 is a key that is not set.
			if err := global(ctx, r, append(in(f), "--unset-all", key)...); err != nil && exitCode(err) != 5 {
				return was, err
			}
		}
		*done[i] = ""
	}
	return was, nil
}
