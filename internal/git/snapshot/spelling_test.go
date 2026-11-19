package snapshot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// foldsCase asks the volume of dir whether it tells letter case apart.
func foldsCase(t *testing.T, dir string) bool {
	t.Helper()
	p := filepath.Join(dir, "case-probe")
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(p)
	a, _ := os.Lstat(p)
	b, err := os.Lstat(filepath.Join(dir, "CASE-PROBE"))
	return err == nil && os.SameFile(a, b)
}

// A path the disk holds under another case, on a volume that does not
// tell case apart, is refused by a snapshot taken for a write of it, and
// so by a restore, which takes one first: git would take the file for
// one not there and write over it. On a volume that tells case apart
// the two are two files, and the restore goes on.
func TestRestoreRefusesAPathOnDiskInAnotherCase(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.r.Write("README", "kept\n")
	f.r.Write("docs/a", "kept\n")
	f.r.Commit("one")
	s := f.take(Options{Reason: "x", Paths: []string{"README", "docs"}})
	// Renamed in case only, as a commit since may have, and edited.
	f.r.Git("rm", "-q", "--cached", "README", "docs/a")
	for _, mv := range [][2]string{{"README", "readme"}, {"docs", "Docs"}} {
		if err := os.Rename(filepath.Join(f.r.Dir, mv[0]), filepath.Join(f.r.Dir, mv[1])); err != nil {
			t.Fatal(err)
		}
	}
	f.r.Git("add", "readme", "Docs/a")
	f.r.Write("readme", "my edit\n")
	f.r.Write("Docs/a", "my edit\n")
	folds := foldsCase(t, f.r.Dir)
	for _, p := range []string{"README", "docs/a"} {
		_, err := Restore(ctx, f.run, f.w, s, []string{p}, Overlay)
		var se *SpellingError
		switch {
		case folds && (!errors.As(err, &se) || se.Path != p || se.OnDisk != map[string]string{"README": "readme", "docs/a": "Docs/a"}[p]):
			t.Fatalf("restore of %s over another case: %v", p, err)
		case !folds && err != nil:
			t.Fatalf("restore of %s: %v", p, err)
		}
	}
	if read(t, filepath.Join(f.r.Dir, "readme")) != "my edit\n" || read(t, filepath.Join(f.r.Dir, "Docs/a")) != "my edit\n" {
		t.Fatal("the restore wrote over the file in another case")
	}
	if !folds && read(t, filepath.Join(f.r.Dir, "README")) != "kept\n" {
		t.Fatal("a volume that tells case apart: README not restored beside readme")
	}
	t.Logf("volume folds case: %v", folds)
}

// A name the disk holds in another Unicode form, as APFS finds it, is
// refused the same way; one spelled as on disk is not.
func TestSpellingInAnotherUnicodeForm(t *testing.T) {
	dir := t.TempDir()
	nfd, nfc := "café", "café"
	if err := os.WriteFile(filepath.Join(dir, nfd), nil, 0o644); err != nil {
		t.Skip(err)
	}
	if err := checkSpelling(dir, []string{nfd + "/x", "absent/" + nfc}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, nfc)); err != nil {
		t.Skip("this volume tells the forms apart")
	}
	var se *SpellingError
	if err := checkSpelling(dir, []string{nfc}); !errors.As(err, &se) || se.OnDisk != nfd {
		t.Fatalf("%v", err)
	}
}
