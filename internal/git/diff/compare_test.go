package diff

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ramus/internal/git/gittest/testrun"
)

func TestCompare(t *testing.T) {
	root := t.TempDir()
	old, new := filepath.Join(root, "old side"), filepath.Join(root, "new side")
	write := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	long := strings.Repeat("the same line\n", 20)
	write(filepath.Join(old, "s", "x"), "1\n")
	write(filepath.Join(new, "s", "x"), "2\n")
	write(filepath.Join(old, "gone"), "y\n")
	write(filepath.Join(new, "日本 語"), "n\n")
	write(filepath.Join(old, "r1"), long)
	write(filepath.Join(new, "r2"), long)
	write(filepath.Join(old, "same"), "s\n")
	write(filepath.Join(new, "same"), "s\n")
	r := testrun.Runner(t)

	c, err := CompareFolders(context.Background(), r, old, new, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for i := range c.Len() {
		got = append(got, fmt.Sprintf("%c %s|%s", c.Status(i), c.Old(i), c.New(i)))
	}
	slices.Sort(got)
	want := []string{"A |日本 語", "D gone|", "M s/x|s/x", "R r1|r2"}
	if !slices.Equal(got, want) || c.Truncated {
		t.Fatalf("compared %q, truncated %v; want %q", got, c.Truncated, want)
	}

	// Cut off inside the last entry, which is left out.
	small, err := CompareFolders(context.Background(), r, old, new, len(old)+4)
	if err != nil || !small.Truncated || small.Len() != 0 {
		t.Fatalf("cut listing: %v, %+v", err, small)
	}

	p, err := ComparePatch(context.Background(), r, filepath.Join(old, "s", "x"), filepath.Join(new, "s", "x"), AllWhitespace, 0)
	if err != nil || !strings.Contains(string(p.Text), "-1\n+2\n") {
		t.Fatalf("patch %q, %v", p.Text, err)
	}
	parsed, err := Parse(p.Text)
	if err != nil || len(parsed.Files) != 1 || len(parsed.Hunks) != 1 {
		t.Fatalf("parsed %+v, %v", parsed, err)
	}
	added, err := ComparePatch(context.Background(), r, "", filepath.Join(new, "日本 語"), AllWhitespace, 0)
	if err != nil || !strings.Contains(string(added.Text), "new file mode") {
		t.Fatalf("added %q, %v", added.Text, err)
	}
	same, err := ComparePatch(context.Background(), r, filepath.Join(old, "same"), filepath.Join(new, "same"), AllWhitespace, 0)
	if err != nil || len(same.Text) != 0 {
		t.Fatalf("same %q, %v", same.Text, err)
	}
}
