package conflict

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"ramus/internal/git/gittest/testrun"
)

func TestMergeFiles(t *testing.T) {
	dir := t.TempDir()
	put := func(name, s string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	base := put("base", "a\nb\nc\nd\ne\nf\n")
	local := put("local", "a\nB1\nc\nd\ne\nF\n")
	remote := put("remote", "a\nB2\nc\nd\ne\nf\n")
	r := testrun.Runner(t)
	labels := [3]string{"x_LOCAL", "x_BASE", "x_REMOTE"}
	f, err := MergeFiles(context.Background(), r, base, local, remote, labels)
	if err != nil || f.Binary || Count(f.Segments) != 1 {
		t.Fatalf("%+v, %v", f, err)
	}
	var cc *Conflict
	for _, s := range f.Segments {
		if s.Conflict != nil {
			cc = s.Conflict
		}
	}
	if string(cc.Ours) != "B1\n" || string(cc.Theirs) != "B2\n" || string(cc.Base) != "b\n" || cc.OursLabel != "x_LOCAL" || cc.TheirsLabel != "x_REMOTE" {
		t.Fatalf("conflict %+v", cc)
	}
	out, err := Resolve(f.Segments, []Choice{Theirs}, nil)
	if err != nil || string(out) != "a\nB2\nc\nd\ne\nF\n" {
		t.Fatalf("resolved %q, %v", out, err)
	}

	// No base: both sides added the file.
	f, err = MergeFiles(context.Background(), r, "", local, remote, labels)
	if err != nil || Count(f.Segments) == 0 {
		t.Fatalf("no base: %+v, %v", f, err)
	}
	clean, err := MergeFiles(context.Background(), r, base, base, remote, labels)
	if err != nil || Count(clean.Segments) != 0 {
		t.Fatalf("clean: %+v, %v", clean, err)
	}
	// Text like conflict markers in the files, in agreed text and in a
	// side of a conflict, is text: the merge's markers are longer.
	lines := "a\nb\nc\nd\ne\nf\ng\nh\n"
	base = put("base", example+lines+"end\n")
	local = put("local", example+lines+"<<<<<<< x\n=======\n")
	remote = put("remote", example+lines+"end remote\n")
	f, err = MergeFiles(context.Background(), r, base, local, remote, labels)
	if err != nil || Count(f.Segments) != 1 {
		t.Fatalf("markers in the files: %+v, %v", f, err)
	}
	for _, side := range []struct {
		flag   string
		choice Choice
	}{{"--ours", Ours}, {"--theirs", Theirs}} {
		want, _ := mergeFile(t, side.flag, local, base, remote)
		if got, err := Resolve(f.Segments, []Choice{side.choice}, nil); err != nil || string(got) != want {
			t.Errorf("markers in the files, %s: %q, %v; want %q", side.flag, got, err, want)
		}
	}
	bin := put("bin", "\x00\x01")
	f, err = MergeFiles(context.Background(), r, base, bin, remote, labels)
	if err != nil || !f.Binary {
		t.Fatalf("binary: %+v, %v", f, err)
	}
}
