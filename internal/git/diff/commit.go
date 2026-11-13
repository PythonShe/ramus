package diff

import (
	"context"
	"encoding/hex"
	"fmt"
	"strconv"

	"ramus/internal/git/catfile"
	"ramus/internal/git/run"
)

// Reader reads objects in batches, as catfile.Process does.
type Reader interface {
	Read(oids [][]byte, fn func(i int, o catfile.Object) error) error
}

// CommitPatch gets the patch of paths between commits or trees a and b
// from git diff, with renames, so diff.algorithm and textconv apply. For a
// rename, pass both paths. contextLines below zero keeps git's diff.context.
//
// The view shows it with the files from BlobPair (DiffState.SetPatchFiles),
// so the whole file can unfold around the hunks. When textconv rewrote the
// file, the patch does not make one blob from the other, and the view
// shows the patch alone.
func CommitPatch(ctx context.Context, r *run.Runner, gitDir string, a, b []byte, paths []string, contextLines, maxBytes int) (Patch, error) {
	args := []string{"diff", "--no-color", "--no-ext-diff", "--submodule=short", "-M"}
	if contextLines >= 0 {
		args = append(args, "-U"+strconv.Itoa(contextLines))
	}
	args = append(args, hex.EncodeToString(a), hex.EncodeToString(b), "--")
	return runPatch(ctx, r, gitDir, "", append(args, paths...), nil, maxBytes, false)
}

// BlobPair reads a changed file's two sides natively, in one batch. A nil
// or zero object id is a side that does not exist, read as empty. A blob
// over catfile.MaxObject is an error: the view shows its patch alone.
func BlobPair(rd Reader, oldID, newID []byte) (old, new []byte, err error) {
	var ids [][]byte
	var which []int
	for k, id := range [][]byte{oldID, newID} {
		if !zero(id) {
			ids, which = append(ids, id), append(which, k)
		}
	}
	var out [2][]byte
	err = rd.Read(ids, func(i int, o catfile.Object) error {
		switch {
		case o.Missing:
			return fmt.Errorf("diff: blob %x is missing", ids[i])
		case o.TooLarge:
			return fmt.Errorf("diff: blob %x is too large to show whole", ids[i])
		case o.Type != "blob":
			return fmt.Errorf("diff: %x is a %s, not a blob", ids[i], o.Type)
		}
		out[which[i]] = append([]byte(nil), o.Data...)
		return nil
	})
	return out[0], out[1], err
}

func zero(id []byte) bool {
	for _, c := range id {
		if c != 0 {
			return false
		}
	}
	return true
}
