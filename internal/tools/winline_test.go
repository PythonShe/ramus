package tools

import (
	"errors"
	"testing"
)

func TestIsBatch(t *testing.T) {
	for p, want := range map[string]bool{
		`C:\VS Code\bin\code.cmd`: true,
		`C:\tools\lint.BAT`:       true,
		`C:\tools\lint.bat.`:      true, // Windows drops the dot
		`C:\tools\lint.cmd  `:     true,
		`C:\tools\lint.exe`:       false,
		`C:\tools\cmd`:            false,
		`/usr/bin/make`:           false,
	} {
		if got := isBatch(p); got != want {
			t.Errorf("isBatch(%q) = %v", p, got)
		}
	}
}

// A file or branch name that holds cmd.exe's own characters reaches a
// batch file as one quoted argument, never as a command; one cmd.exe
// would expand, or that would end the quote, is refused.
func TestBatchLine(t *testing.T) {
	sh := `C:\Windows\System32\cmd.exe`
	got, err := batchLine(sh, []string{`C:\VS Code\bin\code.cmd`, `C:\repo\x&calc&.md`, `C:\repo\a (1)^|<>.txt`, `C:\repo\dir\`, ``, `wip!`})
	want := `"C:\Windows\System32\cmd.exe" /d /e:on /v:off /s /c ""C:\VS Code\bin\code.cmd" "C:\repo\x&calc&.md" "C:\repo\a (1)^|<>.txt" "C:\repo\dir\\" "" "wip!""`
	if err != nil || got != want {
		t.Fatalf("got  %s\nwant %s (%v)", got, want, err)
	}
	for _, bad := range []string{`C:\repo\%PATH%.md`, `x"&calc&"`, "a\nb", "a\rb"} {
		if _, err := batchLine(sh, []string{`C:\t\x.cmd`, bad}); !errors.Is(err, ErrBatchArgument) {
			t.Errorf("%q passed: %v", bad, err)
		}
	}
}

// Explorer gets the path quoted after "/select,", not the two quoted
// together, and Windows Terminal a ; escaped.
func TestWindowsLines(t *testing.T) {
	got, err := revealLine(`C:\Windows\explorer.exe`, `C:\Users\First Last\repo\a,b.txt`)
	if want := `"C:\Windows\explorer.exe" /select,"C:\Users\First Last\repo\a,b.txt"`; err != nil || got != want {
		t.Fatalf("reveal: %s (%v)", got, err)
	}
	if got := wtFolder(`C:\src\a;b`); got != `C:\src\a\;b` {
		t.Fatalf("wt: %s", got)
	}
}
