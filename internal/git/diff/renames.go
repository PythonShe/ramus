package diff

import (
	"context"
	"encoding/hex"
	"regexp"
	"strconv"

	"ramus/internal/git/run"
)

// RenameLimit reports that git skipped rename detection for a diff with
// too many added and deleted files. Needed is the diff.renameLimit git
// asked for; the app shows it with the list, which then has the files as
// deleted and added rather than renamed.
type RenameLimit struct {
	Skipped bool
	Needed  int
}

var limitRE = regexp.MustCompile(`diff\.renameLimit variable to at least (\d+)`)

// Renames lists what changed from commit or tree a to b, with renames,
// through git diff-tree -M so that diff.renames and diff.renameLimit apply
// as in the terminal. For a commit, a is its first parent; nil lists a
// root commit b as all added. It is run for the commit that is open; the
// history list uses the native tree diff.
func Renames(ctx context.Context, r *run.Runner, gitDir string, hashSize int, a, b []byte) (*Raw, RenameLimit, error) {
	args := []string{"diff-tree", "-r", "-z", "--raw", "--no-abbrev", "-M"}
	if a == nil {
		args = append(args, "--root", hex.EncodeToString(b))
	} else {
		args = append(args, hex.EncodeToString(a), hex.EncodeToString(b))
	}
	return renames(ctx, r, &run.Cmd{GitDir: gitDir, Args: args, Read: true, Untranslated: true}, hashSize)
}

// WorkTreeRenames lists what changed from commit or tree a to the work
// tree, with renames, through git diff-index, the plumbing, which never
// writes the index: as git diff a, files git does not track are left
// out. A file changed in the work tree has a zero new id.
func WorkTreeRenames(ctx context.Context, r *run.Runner, gitDir, workTree string, hashSize int, a []byte) (*Raw, RenameLimit, error) {
	args := []string{"diff-index", "-z", "--raw", "--no-abbrev", "-M", hex.EncodeToString(a)}
	return renames(ctx, r, &run.Cmd{GitDir: gitDir, WorkTree: workTree, Args: args, Read: true, Untranslated: true}, hashSize)
}

func renames(ctx context.Context, r *run.Runner, cmd *run.Cmd, hashSize int) (*Raw, RenameLimit, error) {
	res, err := r.Run(ctx, cmd)
	if err != nil {
		return nil, RenameLimit{}, err
	}
	raw, err := ParseRaw(res.Stdout, hashSize)
	if err != nil {
		return nil, RenameLimit{}, err
	}
	var lim RenameLimit
	if m := limitRE.FindStringSubmatch(res.Stderr); m != nil {
		lim.Skipped = true
		lim.Needed, _ = strconv.Atoi(m[1])
	}
	return raw, lim, nil
}
