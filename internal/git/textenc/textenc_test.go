package textenc

import (
	"errors"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
)

// A message encoded for i18n.commitEncoding is what git log, which turns
// it back into UTF-8, prints; the commit's own bytes decode to the same.
func TestCommitEncoding(t *testing.T) {
	for _, tc := range []struct{ enc, msg string }{
		{"ISO-8859-1", "café déjà vu"},
		{"Shift_JIS", "日本語のコミット"},
		{"GBK", "中文提交"},
		{"UTF-8", "plain 🎉"},
	} {
		t.Run(tc.enc, func(t *testing.T) {
			r := gittest.Init(t)
			r.Git("config", "i18n.commitEncoding", tc.enc)
			b, err := Encode(tc.msg, tc.enc)
			if err != nil {
				t.Fatal(err)
			}
			r.Write("f", "x")
			r.Git("add", "f")
			r.Run(b, "commit", "-q", "-F", "-")
			if got := strings.TrimSpace(r.Git("-c", "i18n.logOutputEncoding=UTF-8", "log", "-1", "--format=%s")); got != tc.msg {
				t.Fatalf("git log reads %q", got)
			}
			raw := r.Git("cat-file", "commit", "HEAD")
			_, body, _ := strings.Cut(raw, "\n\n")
			s, ok := Decode([]byte(strings.TrimSpace(body)), tc.enc)
			if !ok || s != tc.msg {
				t.Fatalf("decoded %q, %v", s, ok)
			}
		})
	}
	if _, err := Encode("日本", "ISO-8859-1"); !errors.Is(err, ErrUnrepresentable) {
		t.Fatalf("Latin-1 took Japanese: %v", err)
	}
}

func TestDecodeKeepsBytes(t *testing.T) {
	for _, tc := range []struct {
		in, enc, want string
		ok            bool
	}{
		{"caf\xe9", "", `caf\xe9`, false},
		{"caf\xe9", "no-such-encoding", `caf\xe9`, false},
		{"ok ü", "", "ok ü", true},
		{"ok ü", "utf8", "ok ü", true},
		{"\xff\xfe x", "", `\xff\xfe x`, false},
	} {
		got, ok := Decode([]byte(tc.in), tc.enc)
		if got != tc.want || ok != tc.ok {
			t.Errorf("Decode(%q, %q) = %q, %v; want %q, %v", tc.in, tc.enc, got, ok, tc.want, tc.ok)
		}
	}
}
