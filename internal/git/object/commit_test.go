package object

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ramus/internal/git/catfile"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

func TestParseCommit(t *testing.T) {
	data := []byte("tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\n" +
		"parent 1111111111111111111111111111111111111111\n" +
		"parent 2222222222222222222222222222222222222222\n" +
		"author A U Thor <a@example.com> 1700000000 +0530\n" +
		"committer C O Mitter <c@example.com> 1700000100 -0800\n" +
		"encoding ISO-8859-1\n" +
		"gpgsig -----BEGIN PGP SIGNATURE-----\n" +
		" abc\n" +
		" -----END PGP SIGNATURE-----\n" +
		"\n" +
		"Subject line\ncontinues\n\nBody.\n")
	c, err := ParseCommit(data)
	if err != nil {
		t.Fatal(err)
	}
	if c.Parents != 2 || string(c.Author.Name) != "A U Thor" || string(c.Author.Email) != "a@example.com" ||
		c.Author.When != 1700000000 || c.Author.Offset != 330 || c.Committer.Offset != -480 ||
		string(c.Encoding) != "ISO-8859-1" || !c.Signed {
		t.Fatalf("%+v", c)
	}
	if s := string(Subject(c.Message)); s != "Subject line continues" {
		t.Fatalf("subject %q", s)
	}
	if _, err := ParseCommit([]byte("garbage")); err == nil {
		t.Fatal("parsed garbage")
	}
}

// TestOracle compares every commit of a fixture, and of the kernel when
// RAMUS_FIXTURES is set, with git log's view of it.
func TestOracle(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a", "1")
	r.Commit("plain")
	r.Write("a", "2")
	r.Commit("multi\nline\nsubject\n\nbody")
	r.Write("a", "3")
	r.Commit("  leading spaces")
	compare(t, r, 0)
}

func TestOracleKernel(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	dir := filepath.Join(gittest.FixturesDir(t), "linux")
	if _, err := os.Stat(dir); err != nil {
		t.Skip(err)
	}
	compare(t, gittest.Open(t, dir), 20000)
}

func compare(t *testing.T, r *gittest.Repo, limit int) {
	t.Helper()
	args := []string{"log", "--all", "--format=%H%x00%an%x00%ae%x00%at%x00%cn%x00%ct%x00%s%x00"}
	if limit > 0 {
		args = append(args, "-n", strconv.Itoa(limit))
	}
	out := r.Git(args...)
	p, err := catfile.Start(context.Background(), testrun.Runner(t), r.GitDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	var want [][]string
	var ids [][]byte
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		f := strings.Split(line, "\x00")
		want = append(want, f)
		id, _ := hex.DecodeString(f[0])
		ids = append(ids, id)
	}
	err = p.Read(ids, func(i int, o catfile.Object) error {
		c, err := ParseCommit(o.Data)
		if err != nil {
			return err
		}
		got := []string{want[i][0], string(c.Author.Name), string(c.Author.Email), fmt.Sprint(c.Author.When),
			string(c.Committer.Name), fmt.Sprint(c.Committer.When), string(Subject(c.Message)), ""}
		if c.Encoding == nil && strings.Join(got, "\x00") != strings.Join(want[i], "\x00") {
			return fmt.Errorf("commit %s:\n got %q\nwant %q", want[i][0], got, want[i])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
