package ui

import (
	"strings"
	"testing"
)

// A comment is text anyone on the host may write, read as the pull
// request is shown: whatever it holds, reading it neither panics nor
// makes more of it than a bounded multiple of what it is.
func FuzzCommentMarkdown(f *testing.F) {
	for _, s := range []string{
		"", "-", "*", "+", "1.", "12)", "text\n-", "- a\n-\n- b", "-\t", "1.\t", "123456789.", "1234567890.",
		"Text\n    ~~~\n\n<details>x</details>", "-     <details>x</details>", "-    x", "- ```\n<details>\n```",
		"```", "~~~", "````\n```\n", "``` `x`", "```go\n<details>\n", "\t```\n", "  - ~~~\n    x",
		"<details>", "</details>", "<details><summary>", "<details><summary>a</summary>",
		strings.Repeat("<details>", 10) + strings.Repeat("</details>", 3), "<details open>x</details>",
		"<DETAILS><SUMMARY>S</SUMMARY>x</DETAILS>", "a</details>b", "<details>\n```\n</details>\n```\n</details>",
		"`", "``", "`a", "a ``b` c", "<img src=x>", "<img alt=\"a\" src=\"https://e/x.png\">", "<a href=\"https://e\">x</a>",
		"<table><tr><td>a</td></tr></table>", "<h1>x</h1>", "<!-- c -->", "<br>", "&amp;&lt;",
		"a\r\nb\r\n- c\r\n", "\x00", "a\x00<details>\x00", "\xff\xfe<details>\xc3", "- \xff",
		"ff00aa", "+12345", "-12345", "0x1234", "fff", "https://user@e/x", "http://[::1", "javascript:alert(1)",
		strings.Repeat("a", 1<<12), strings.Repeat("-\n", 500), strings.Repeat("    x\n", 200), strings.Repeat("`", 300),
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 1<<16 {
			return
		}
		parts := commentParts(s, "Image")
		if n := partsLen(parts); n > 8*len(s)+256 {
			t.Fatalf("%d bytes read into %d", len(s), n)
		}
		for _, r := range codeRanges(s) {
			if r[0] < 0 || r[0] > r[1] || r[1] > len(s) {
				t.Fatalf("code range %v of %d bytes", r, len(s))
			}
		}
		// A byte that is not UTF-8 is shown as its escape, \xNN.
		if n := len(shownBody(s)); n > 4*len(s) {
			t.Fatalf("%d bytes shown as %d", len(s), n)
		}
		// The host's other text read here: a label's color and a link's
		// address.
		labelColor(s)
		webLink(s)
	})
}

// partsLen is the text parts holds, in bytes.
func partsLen(parts []*commentPart) int {
	n := 0
	for _, p := range parts {
		n += len(p.md) + len(p.summary) + partsLen(p.parts)
	}
	return n
}
