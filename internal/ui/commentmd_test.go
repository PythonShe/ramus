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
		{"a fence in a list item", "1. Add this:\n\n    ~~~html\n    <details>\n    <summary>More</summary>\n    ~~~\n\n2. Then run the tests.\n\nThe rest.",
			"[1. Add this:\n\n    ~~~html\n    <details>\n    <summary>More</summary>\n    ~~~\n\n2. Then run the tests.\n\nThe rest.]"},
		{"a fence in a nested item", "- a\n  1. b\n\n         ```\n         <details>\n         ```\n- c<details>d</details>",
			"[- a\n  1. b\n\n         ```\n         <details>\n         ```\n- c]{: [d]}"},
		{"indented code", "Example:\n\n    <details>\n\tx\n\nAfter the code block.", "[Example:\n\n    <details>\n\tx\n\nAfter the code block.]"},
		{"indented, not code", "Text\n    <details>x</details>", "[Text]{: [x]}"},
		{"indented, after a fold", "<details><summary>S</summary>\n\n    <details>\n\n</details>", "{S: [<details>]}"},
		// A marker with no text after it is an empty item.
		{"a marker alone", "-", "[-]"},
		{"markers alone", "*\n+\n1.\n12)\ntext\n-\n- a\n-\n- b<details>x</details>", "[*\n+\n1.\n12)\ntext\n-\n- a\n-\n- b]{: [x]}"},
		// Four columns past a paragraph go on it: no fence opens there.
		{"a fence indented four", "Text\n    ~~~\n\n<details>x</details>", "[Text\n    ~~~]{: [x]}"},
		{"a fence indented three", "Text\n   ~~~\n\n<details>x</details>", "[Text\n   ~~~\n\n<details>x</details>]"},
		// Five spaces past a marker are code in the item.
		{"code after a marker", "-     <details>x</details>", "[-     <details>x</details>]"},
		{"text after a marker", "-    <details>x</details>", "[-]{: [x]}"},
		// A tab after a marker and a space goes to column four, two past
		// the marker: a fence opens.
		{"a fence after a space and a tab", "- \t~~~\n    <details><summary>x</summary>literal</details>\n    ~~~\n",
			"[- \t~~~\n    <details><summary>x</summary>literal</details>\n    ~~~]"},
		{"a fence after a number, a space and a tab", "1. \t```\n    <details><summary>x</summary>literal</details>\n    ```\n",
			"[1. \t```\n    <details><summary>x</summary>literal</details>\n    ```]"},
		// Two tabs go to column eight: code in the item.
		{"code after two tabs", "-\t\t<details>x</details>", "[-\t\t<details>x</details>]"},
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
		{"code in a list item kept", "1. Run:\n\n    ~~~\n    <p>x</p>\n    ~~~\n2. <hr>", "1. Run:\n\n    ~~~\n    <p>x</p>\n    ~~~\n2. \n\n---"},
		{"indented code kept", "Example:\n\n    <a href=\"y\">y</a>\n\n<b>b</b>", "Example:\n\n    <a href=\"y\">y</a>\n\n<b>b</b>"},
	} {
		if got := strings.TrimSpace(tidyComment(c.in, "Image")); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

// A comment as long as a host allows (GitHub 64 KiB, GitLab a million
// characters) is read in a time that grows with its length, whatever it
// holds: one that took the square of it froze the window for minutes.
// One comment of a mebibyte is timed against sixteen of a sixteenth, which
// take as long when the time grows with the length and a sixteenth of it
// when it grows with the square, on a machine however slow or loaded: a
// bound in seconds failed on a busy one that read fourteen times slower.
func TestLongCommentsReadInLinearTime(t *testing.T) {
	const size, pieces = 1 << 20, 16
	bodies := func(size int) map[string]string {
		repeat := func(unit string) string { return strings.Repeat(unit, size/len(unit)) }
		return map[string]string{
			"code and bangs":     repeat("`a` !"),
			"code and tags":      repeat("`a` <"),
			"code and folds":     repeat("`a` <details>"),
			"unclosed summaries": repeat("<details><summary>x"),
			"backticks never closed": strings.Repeat("x", size/2) + func() string {
				var b strings.Builder
				for w := 1; b.Len() < size/2; w++ {
					b.WriteString(strings.Repeat("`", w) + "<a")
				}
				return b.String()
			}(),
			"pre with code": repeat("<pre>`a` `b`</pre> `c` "),
		}
	}
	read := func(body string, times int) time.Duration {
		start := time.Now()
		for range times {
			commentParts(body, "Image")
		}
		return time.Since(start)
	}
	small := bodies(size / pieces)
	for name, body := range bodies(size) {
		parts, whole := read(small[name], pieces), read(body, 1)
		// Four times leaves room for what else the machine does; the
		// square is sixteen.
		if whole > 4*parts {
			t.Errorf("%s: %d bytes read in %v, %d comments of a sixteenth in %v", name, len(body), whole, pieces, parts)
		}
	}
}

// A long comment is read in the background, not in a frame, and once:
// the review read again after a refresh or a write keeps the comments it
// still has read, within a budget.
func TestCommentsReadOnceOffTheFrame(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	h.app.Open(r.Dir)
	h.until("the tab", func() bool { return h.app.current() != nil })
	tb := h.app.current()
	rv := &prReview{}
	long := strings.Repeat("`a` !", 1<<20/5) + "\n\nthe end"
	p := h.app.comment(tb, rv, long)
	if p.ready {
		t.Fatal("a long comment read within the frame")
	}
	if h.app.comment(tb, rv, long) != p {
		t.Fatal("a comment being read read again")
	}
	h.until("the long comment read", func() bool { return p.ready })
	if n := len(p.parts); n != 1 || !strings.HasSuffix(p.parts[0].md, "the end") {
		t.Fatalf("%d parts", n)
	}
	short := h.app.comment(tb, rv, "<!-- hidden -->")
	if !short.ready || !short.empty {
		t.Fatalf("a short comment %+v", short)
	}

	rv.bodies.keep(&forge.Review{Comments: []forge.Comment{{Body: long}}})
	if h.app.comment(tb, rv, long) != p || len(rv.bodies.m) != 1 {
		t.Fatalf("read again: %d kept", len(rv.bodies.m))
	}
	rv.bodies.keep(&forge.Review{Body: "other"})
	if len(rv.bodies.m) != 0 || rv.bodies.bytes != 0 {
		t.Fatalf("a comment gone kept: %d, %d bytes", len(rv.bodies.m), rv.bodies.bytes)
	}
	plain := strings.Repeat("x", 1<<20)
	for i := range 2 * commentBudget / len(plain) {
		h.app.comment(tb, rv, plain+strings.Repeat(" ", i))
		if rv.bodies.bytes > commentBudget {
			t.Fatalf("%d bytes kept", rv.bodies.bytes)
		}
	}
	h.until("the rest read", func() bool {
		for _, p := range rv.bodies.m {
			if !p.ready {
				return false
			}
		}
		return true
	})
}

// The code a comment's HTML rewriting holds back is put back where it
// was, code within a <pre> too.
func TestTidyCommentPutsCodeBack(t *testing.T) {
	in := "a `<b>` c <pre>x `y` &lt;z&gt;</pre> `<i>` <p>"
	want := "a `<b>` c \n\n```\nx `y` <z>\n```\n\n `<i>`"
	if got := strings.TrimSpace(tidyComment(in, "Image")); got != want {
		t.Errorf("got %q\nwant %q", got, want)
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
