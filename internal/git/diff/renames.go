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
	res, err := r.Run(ctx, &run.Cmd{GitDir: gitDir, Args: args, Read: true, Untranslated: true})
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
