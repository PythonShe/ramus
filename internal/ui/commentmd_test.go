package ui

import (
	"strings"
	"testing"
	"time"

	"ramus/internal/forge"
)

// shape writes parts as text: Markdown as it is, a fold as its summary
// around what it holds.
func shape(parts []*commentPart) string {
	var b strings.Builder
	for _, p := range parts {
		if !p.fold {
			b.WriteString("[" + strings.TrimSpace(p.md) + "]")
			continue
		}
		open := ""
		if p.open {
			open = "+"
		}
		b.WriteString("{" + open + p.summary + ": " + shape(p.parts) + "}")
	}
	return b.String()
}

// GitHub's folds become parts of their own, with their summaries; what
// is code is left as written.
func TestCommentFolds(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"none", "Just **text**.", "[Just **text**.]"},
		{"one", "Before\n\n<details>\n<summary>More <b>here</b> &amp; there</summary>\n<br />\n\nInside\n\n</details>\n\nAfter",
			"[Before]{More here & there: [Inside]}[After]"},
		{"open, no summary", "<details open>\nInside\n</details>", "{+: [Inside]}"},
		{"upper case", "<DETAILS><SUMMARY>S</SUMMARY>x</DETAILS>", "{S: [x]}"},
		{"nested", "<details><summary>A</summary>\n\na\n\n<details><summary>B</summary>\n\nb\n\n</details>\n\n</details>",
			"{A: [a]{B: [b]}}"},
		{"never closed", "x\n<details><summary>A</summary>\n\nrest", "[x]{A: [rest]}"},
		{"closed twice", "a</details>b", "[a][b]"},
		{"in a fence", "```html\n<details><summary>A</summary></details>\n```", "[```html\n<details><summary>A</summary></details>\n```]"},
		{"in code", "Write `<details>` to fold.", "[Write `<details>` to fold.]"},
		{"a fence inside", "<details><summary>Log</summary>\n\n```\n</details>\n```\n\n</details>end",
			"{Log: [```\n</details>\n```]}[end]"},
		{"empty", "<details></details>", "{: }"},
	} {
		if got := shape(commentParts(c.in, "Image")); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
	}
	// Folds within folds without end are not followed down.
	deep := strings.Repeat("<details>", 50) + "x" + strings.Repeat("</details>", 50)
	depth := 0
	for p := commentParts(deep, "Image"); len(p) > 0 && p[0].fold; p = p[0].parts {
		depth++
	}
	if depth != maxFolds {
		t.Errorf("%d folds deep", depth)
	}
	// Nor is every fold of a comment made of them built: the rest read
	// on, where they are.
	many := commentParts(strings.Repeat("<details><summary>s</summary>x</details>\n", maxFoldCount+20)+"end", "Image")
	n := 0
	for _, p := range many {
		if p.fold {
			n++
		}
	}
	if last := many[len(many)-1]; n != maxFoldCount || last.fold || !strings.Contains(last.md, "end") || strings.Contains(last.md, "<") {
		t.Errorf("%d folds, then %q", n, last.md)
	}
}

// The HTML comments are written with becomes its Markdown, since the
// view shows HTML it does not know as its source.
func TestTidyComment(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"text", "No markup, 1 < 2.", "No markup, 1 < 2."},
		{"picture", "![Build status](https://ci.example/badge.svg)", "[Build status](https://ci.example/badge.svg)"},
		{"picture unnamed", "![](https://x.example/a.png \"title\")", "[Image](https://x.example/a.png)"},
		{"picture that is a link", "[![Score](https://x.example/s.svg)](https://x.example/why)", "[Score](https://x.example/why)"},
		{"picture with parentheses", "![plot](https://x.example/plot(1).png) after", "[plot](<https://x.example/plot(1).png>) after"},
		{"picture in angle brackets", "![a plot](<https://x.example/a plot.png>)", "[a plot](<https://x.example/a plot.png>)"},
		{"linked picture with parentheses", "[![b](https://x.example/x(1).svg)](https://x.example/a_(b) 'why')", "[b](<https://x.example/a_(b)>)"},
		{"img", `See <img width="400" alt="The dialog" src="https://x.example/a b.png" />.`, "See [The dialog](<https://x.example/a b.png>)."},
		{"img unnamed", `<img src='https://x.example/a.png'>`, "[Image](https://x.example/a.png)"},
		{"anchor", `Read <a href="https://x.example/?a=1&amp;b=2" target="_blank">the <b>docs</b></a>.`, "Read [the <b>docs</b>](https://x.example/?a=1&b=2)."},
		{"anchor around img", `<a href="https://x.example/"><img src="https://x.example/i.png" alt="Logo"></a>`, "[Logo](https://x.example/)"},
		{"anchor unnamed", `<a href="https://x.example/"></a>`, "[https://x.example/](https://x.example/)"},
		{"heading", "<h3 align=\"center\">A  title</h3>\ntext", "### A title\n\ntext"},
		{"blocks", "<p align=\"center\">\n    <b>Centered</b>\n</p>\n<div>next</div>", "<b>Centered</b>\n\nnext"},
		{"list", "<ul>\n  <li>one</li>\n  <li>two</li>\n</ul>", "- one\n- two"},
		{"rule and break", "a\n<hr>\n<br/>\nb<br>c", "a\n\n---\n\nb<br>c"},
		{"pre", "<pre><code>if a &lt; b {\n  **x**\n}</code></pre>", "```\nif a < b {\n  **x**\n}\n```"},
		{"table", "<table>\n<tr><th>Name</th><th>State</th></tr>\n<tr><td>a|b</td><td><p>ok</p></td></tr>\n<tr><td>short</td></tr>\n</table>",
			"| Name | State |\n| --- | --- |\n| a\\|b | ok |\n| short |  |"},
		{"table without rows", "<table></table>", "<table></table>"},
		{"code kept", "Use `<img src=x>` or\n\n```html\n<a href=\"y\">y</a>\n![z](z)\n```\n\n<hr>", "Use `<img src=x>` or\n\n```html\n<a href=\"y\">y</a>\n![z](z)\n```\n\n\n---"},
		{"formatting kept", "H<sub>2</sub>O, <kbd>Ctrl</kbd>", "H<sub>2</sub>O, <kbd>Ctrl</kbd>"},
	} {
		if got := strings.TrimSpace(tidyComment(c.in, "Image")); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestCodeRanges(t *testing.T) {
	s := "a `b` c ``d ` e`` f\n~~~\n`x`\n~~~\ng `h\n```go\nfn\n"
	var got []string
	for _, r := range codeRanges(s) {
		got = append(got, s[r[0]:r[1]])
	}
	want := []string{"`b`", "``d ` e``", "~~~\n`x`\n~~~\n", "```go\nfn\n"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("code %q", got)
	}
}

// What is open in a comment stays with it when the pull request is read
// again and a comment before it is gone, and goes with a comment that
// is.
func TestFoldsStayWithTheirComment(t *testing.T) {
	at := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	rv := &prReview{}
	rv.r.Number = 1
	rv.r.Comments = []forge.Comment{{ID: "A", Author: "ann", At: at}, {ID: "B", Author: "bob", At: at.Add(time.Hour)}}
	rv.r.Reviews = []forge.Submitted{{Author: "cat", State: forge.ReviewApproved, Body: "Fine", At: at.Add(2 * time.Hour)}}
	ev := rv.timeline()
	seen := map[string]bool{}
	for _, e := range ev {
		if e.id == "" || seen[e.id] {
			t.Fatalf("entries told apart by %q", e.id)
		}
		seen[e.id] = true
	}
	gone, kept := ev[1].id, ev[2].id
	open := true
	rv.folds = map[string]map[string]*bool{gone: {"/0": &open}, kept: {"/0": &open}}

	rv.r.Comments = rv.r.Comments[1:]
	rv.events = nil
	ev = rv.timeline()
	if ev[1].id != kept || rv.folds[kept]["/0"] != &open || len(rv.folds) != 1 {
		t.Fatalf("the second comment is %q, was %q; folds %v", ev[1].id, kept, rv.folds)
	}
}
