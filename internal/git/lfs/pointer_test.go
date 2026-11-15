package lfs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func pointerOf(content string) (string, Pointer) {
	sum := sha256.Sum256([]byte(content))
	p := Pointer{OID: hex.EncodeToString(sum[:]), Size: int64(len(content))}
	return "version https://git-lfs.github.com/spec/v1\noid sha256:" + p.OID + "\nsize " + itoa(p.Size) + "\n", p
}

func itoa(n int64) string { return string(rune('0'+n/10%10)) + string(rune('0'+n%10)) }

func TestParsePointer(t *testing.T) {
	text, want := pointerOf("hello, large file")
	if got, ok := ParsePointer([]byte(text)); !ok || got != want {
		t.Fatalf("got %+v %v, want %+v", got, ok, want)
	}
	// Another key, as git-lfs's extensions add, sorts among them.
	ext := "version https://git-lfs.github.com/spec/v1\next-0-foo sha256:" + want.OID + "\noid sha256:" + want.OID + "\nsize 17\n"
	if got, ok := ParsePointer([]byte(ext)); !ok || got != want {
		t.Fatalf("with an extension: %+v %v", got, ok)
	}
	for _, bad := range []string{
		"",
		"hello\n",
		"version https://git-lfs.github.com/spec/v1\nsize 3\n",
		"version https://git-lfs.github.com/spec/v1\noid sha256:" + want.OID + "\n",
		"version https://git-lfs.github.com/spec/v1\noid sha256:ABC\nsize 3\n",
		"version https://git-lfs.github.com/spec/v1\noid sha256:" + want.OID + "\nsize -1\n",
		"version https://git-lfs.github.com/spec/v1\noid md5:" + want.OID + "\nsize 3\n",
	} {
		if _, ok := ParsePointer([]byte(bad)); ok {
			t.Errorf("read %q as a pointer", bad)
		}
	}
}

func TestReadObject(t *testing.T) {
	common := t.TempDir()
	const content = "hello, large file"
	_, p := pointerOf(content)
	if _, err := ReadObject(common, p, 1<<20); !errors.Is(err, ErrNotFetched) {
		t.Fatalf("not there: %v", err)
	}
	dir := filepath.Join(common, "lfs", "objects", p.OID[:2], p.OID[2:4])
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, p.OID), []byte(content[:5]), 0o644)
	if _, err := ReadObject(common, p, 1<<20); !errors.Is(err, ErrNotFetched) {
		t.Fatalf("half written: %v", err)
	}
	os.WriteFile(filepath.Join(dir, p.OID), []byte(content), 0o644)
	if b, err := ReadObject(common, p, 1<<20); err != nil || string(b) != content {
		t.Fatalf("got %q, %v", b, err)
	}
	if _, err := ReadObject(common, p, 4); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("past max: %v", err)
	}
}
