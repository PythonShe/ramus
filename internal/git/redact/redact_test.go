package redact

import (
	"strings"
	"testing"
)

func TestControl(t *testing.T) {
	for in, want := range map[string]string{
		"plain text\n":                   "plain text\n",
		"\x1b[31mred\x1b[0m":             "red",
		"title\x1b]0;evil title\x07done": "titledone",
		"osc\x1b]8;;http://x\x1b\\link":  "osclink",
		"bell\x07 back\x08space":         "bell backspace",
		"progress 10%\rprogress 20%\r\n": "progress 10%\nprogress 20%\n",
		"bidi \u202egnp.exe":             "bidi gnp.exe",
		"c1 \u009b31m csi":               "c1  csi",
		"tab\tkept":                      "tab\tkept",
		"\x1b(Bcharset":                  "charset",
		"unterminated \x1b]0;title":      "unterminated ",
		"bad utf8 \xff":                  "bad utf8 \ufffd",
		"\u65e5\u672c\u8a9e":             "\u65e5\u672c\u8a9e",
	} {
		if got := Control(in); got != want {
			t.Errorf("Control(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSecrets(t *testing.T) {
	for in, gone := range map[string]string{
		"fatal: unable to access 'https://user:s3cret@github.com/x.git/'": "s3cret",
		"remote: https://ghp_abcdefghijklmnopqrstuvwxyz0123@host":         "ghp_abc",
		"Authorization: Bearer abc.def.ghi":                               "abc.def",
		"> authorization: Basic dXNlcjpwYXNz":                             "dXNlcjpwYXNz",
		"protocol=https\nhost=x\nusername=me\npassword=hunter2\n":         "hunter2",
		"token in text github_pat_11ABCDEFG0123456789_abcdefghijklmnop":   "github_pat_",
		"git -c http.extraHeader=Authorization: token zzz fetch":          "zzz",
		"glpat-abcdefghijklmnopqrst1":                                     "glpat-",
		"aws AKIAABCDEFGHIJKLMNOP":                                        "AKIAABCD",
	} {
		got := Secrets(in)
		if strings.Contains(got, gone) || !strings.Contains(got, Hidden) {
			t.Errorf("Secrets(%q) = %q", in, got)
		}
	}
	if got := Secrets("Merge branch 'password-reset'"); got != "Merge branch 'password-reset'" {
		t.Errorf("redacted ordinary text: %q", got)
	}
}

func TestHost(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/a/b.git":          "github.com",
		"https://github.com@evil.example/a/b": "evil.example",
		"https://user:pw@host.example:8443/x": "host.example",
		"ssh://git@[::1]:22/x":                "::1",
		"git@github.com:a/b.git":              "github.com",
		"github.com@evil.example:a/b":         "evil.example",
		"/local/path":                         "",
		"./rel:with/colon":                    "",
		"C:\\repo":                            "",
	} {
		got, ok := Host(in)
		if got != want || ok != (want != "") {
			t.Errorf("Host(%q) = %q %v, want %q", in, got, ok, want)
		}
	}
}

func TestCheckRemoteURL(t *testing.T) {
	for _, bad := range []string{"", "-oProxyCommand=x", "https://x\n[core]", "https://x\x00y", "https://x\u2028y"} {
		if CheckRemoteURL(bad) == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	for _, good := range []string{"https://github.com/a/b.git", "git@host:a/b", "/srv/repo.git", "https://h/\u65e5\u672c"} {
		if err := CheckRemoteURL(good); err != nil {
			t.Errorf("rejected %q", good)
		}
	}
}

func FuzzControl(f *testing.F) {
	f.Add("\x1b[31mx\x1b]0;t\x07\u202e")
	f.Fuzz(func(t *testing.T, s string) {
		out := Control(s)
		for _, r := range out {
			if (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f || (r >= 0x80 && r <= 0x9f) || (r >= 0x202a && r <= 0x202e) {
				t.Fatalf("Control(%q) kept %U", s, r)
			}
		}
	})
}
