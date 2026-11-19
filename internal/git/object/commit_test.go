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
		string(c.Encoding) != "ISO-8859-1" || !c.Signed || c.SigKind != SigOpenPGP {
		t.Fatalf("%+v", c)
	}
	for armor, want := range map[string]SigKind{
		"-----BEGIN SSH SIGNATURE-----": SigSSH, "-----BEGIN SIGNED MESSAGE-----": SigX509, "-----BEGIN SOMETHING-----": SigUnknown,
	} {
		ssh := []byte("tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\ngpgsig " + armor + "\n abc\n\nmsg\n")
		if c, err := ParseCommit(ssh); err != nil || !c.Signed || c.SigKind != want {
			t.Fatalf("%s: %+v %v", armor, c, err)
		}
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
	// Messages as tools other than git commit write them: commit-tree
	// keeps them as given.
	tree := strings.TrimSpace(r.Git("rev-parse", "HEAD^{tree}"))
	for i, msg := range []string{
		"Fix the thing\r\n\r\nLong body line one\r\nline two\r\n",
		"Subject  \n   \nbody after a blank-looking line\n",
		"\n \t\n\r\nafter blank lines\nsecond\t \n\nbody",
		"tab\tinside  \r\nnext line\r\n",
		"vertical\v\n\fform feed\n\v\nbody",
		"no newline at all",
		"",
	} {
		id, err := r.Run([]byte(msg), "commit-tree", tree)
		if err != nil {
			t.Fatal(err)
		}
		r.Git("tag", fmt.Sprintf("m%d", i), strings.TrimSpace(id))
	}
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

func FuzzParseCommit(f *testing.F) {
	f.Add([]byte("tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\nauthor A U Thor <a@example.com> 1700000000 +0100\ncommitter C <c@example.com> 1700000000 -0530\n\nsubject\n\nbody\n"))
	f.Add([]byte("author <> x y\n\n"))
	f.Add([]byte("author Name <mail 99999999999999999999 +9999\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		c, err := ParseCommit(data)
		if err != nil {
			return
		}
		_ = Subject(c.Message)
	})
}

func TestParentIDs(t *testing.T) {
	data := []byte("tree aa\nparent bb\nparent cc\nauthor A <a> 1 +0000\n\nparent dd is in the message\n")
	got := ParentIDs(data)
	if len(got) != 2 || string(got[0]) != "bb" || string(got[1]) != "cc" {
		t.Fatalf("%q", got)
	}
}

// git reads parents only from the lines right after the tree: one named
// after the author line is no parent.
func TestParentsAfterTheAuthorLine(t *testing.T) {
	r := gittest.Init(t)
	base := r.Commit("base")
	tree := strings.TrimSpace(r.Git("rev-parse", "HEAD^{tree}"))
	for _, header := range []string{
		"tree " + tree + "\nauthor A <a@x> 1 +0000\nparent " + base + "\ncommitter A <a@x> 1 +0000\n",
		"tree " + tree + "\nparent " + base + "\nauthor A <a@x> 1 +0000\nparent " + base + "\ncommitter A <a@x> 1 +0000\n",
		"tree " + tree + "\nparent " + base + "\nparent " + base + "\nauthor A <a@x> 1 +0000\ncommitter A <a@x> 1 +0000\n",
	} {
		data := []byte(header + "\nmessage\n")
		out, err := r.Run(data, "hash-object", "-t", "commit", "-w", "--literally", "--stdin")
		if err != nil {
			t.Fatal(err)
		}
		want := strings.Fields(r.Git("rev-list", "--parents", "-n", "1", strings.TrimSpace(out)))[1:]
		var got []string
		for _, p := range ParentIDs(data) {
			got = append(got, string(p))
		}
		c, err := ParseCommit(data)
		if err != nil || strings.Join(got, " ") != strings.Join(want, " ") || c.Parents != len(want) {
			t.Errorf("%q: parents %q, %d counted, git %q", header, got, c.Parents, want)
		}
	}
}
