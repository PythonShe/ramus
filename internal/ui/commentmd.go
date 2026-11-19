package ui

import (
	"html"
	"regexp"
	"strconv"
	"strings"
)

// commentPart is a stretch of a comment as it is shown: Markdown, or one
// of GitHub's folds (<details>) with its summary and what it holds.
type commentPart struct {
	md string

	fold    bool
	summary string
	// open is a fold shown open until it is closed, as <details open>.
	open  bool
	parts []*commentPart
}

// maxFolds bounds the folds within folds a comment is shown with, and
// maxFoldCount all those of one comment: past them a fold is part of
// what it is in, since a frame costs every fold it builds.
const (
	maxFolds     = 6
	maxFoldCount = 100
)

var (
	detailsTag = regexp.MustCompile(`(?i)<(/?)details(\s[^>]*)?>`)
	summaryTag = regexp.MustCompile(`(?is)^\s*<summary(?:\s[^>]*)?>(.*?)</summary\s*>`)
	openAttr   = regexp.MustCompile(`(?i)\sopen(\s|=|$)`)
	anyTag     = regexp.MustCompile(`(?s)<[^>]*>`)
	spaces     = regexp.MustCompile(`\s+`)
)

// commentParts reads body, a comment's Markdown as a host holds it, into
// what is shown of it. The Markdown view shows HTML it does not know as
// its source, and comments hold much of it: folds, which become parts of
// their own, and links, pictures, headings and tables written as HTML,
// which become their Markdown. A picture is not fetched: it is a link to
// itself, named by its description, or image when it has none.
func commentParts(body, image string) []*commentPart {
	// The tags not in code. Both come in order, so the code is walked
	// once, not from its start for each tag.
	code := codeRanges(body)
	var tags [][]int
	ci := 0
	for _, m := range detailsTag.FindAllStringSubmatchIndex(body, -1) {
		for ci < len(code) && code[ci][1] <= m[0] {
			ci++
		}
		if ci == len(code) || m[0] < code[ci][0] {
			tags = append(tags, m)
		}
	}
	root := &commentPart{}
	stack := []*commentPart{root}
	last, folds := 0, 0
	// dropped counts the folds left out that are still to close.
	dropped := 0
	flush := func(end int) {
		if md := tidyComment(body[last:end], image); strings.TrimSpace(md) != "" {
			top := stack[len(stack)-1]
			top.parts = append(top.parts, &commentPart{md: md})
		}
	}
	for i, m := range tags {
		if m[0] < last {
			continue
		}
		closing := m[3] > m[2]
		switch {
		case !closing && (len(stack) > maxFolds || folds == maxFoldCount):
			// A fold too deep, or one too many: left out, what it holds
			// staying where it is, and so its end.
			dropped++
			fallthrough
		case closing && len(stack) == 1 && dropped == 0:
			// A tag with nothing to close.
			flush(m[0])
			last = m[1]
			continue
		case closing && dropped > 0:
			dropped--
			flush(m[0])
			last = m[1]
			continue
		}
		flush(m[0])
		last = m[1]
		if closing {
			stack = stack[:len(stack)-1]
			continue
		}
		folds++
		p := &commentPart{fold: true, open: m[4] >= 0 && openAttr.MatchString(body[m[4]:m[5]])}
		// Its summary comes before the next fold's tag: looked for to the
		// end, an unclosed summary in each of many folds would read the
		// rest of the text again for each.
		end := len(body)
		if i+1 < len(tags) {
			end = tags[i+1][0]
		}
		if s := summaryTag.FindStringSubmatchIndex(body[last:end]); s != nil {
			p.summary = plainText(body[last+s[2] : last+s[3]])
			last += s[1]
		}
		top := stack[len(stack)-1]
		top.parts = append(top.parts, p)
		stack = append(stack, p)
	}
	flush(len(body))
	return root.parts
}

// plainText is s without its tags, as one line.
func plainText(s string) string {
	return strings.TrimSpace(spaces.ReplaceAllString(html.UnescapeString(anyTag.ReplaceAllString(s, "")), " "))
}

// ranges are stretches of a text, in order.
type ranges [][2]int

// codeRanges are the stretches of s that are code, which is shown as
// written: fenced blocks, and the code within a line.
func codeRanges(s string) ranges {
	var out ranges
	fenced := fencedRanges(s)
	at := 0
	for _, f := range append(fenced, [2]int{len(s), len(s)}) {
		out = append(out, inlineCode(s, at, f[0])...)
		if f[1] > f[0] {
			out = append(out, f)
		}
		at = f[1]
	}
	return out
}

// fencedRanges are the fenced code blocks of s, their fences included.
// One never closed runs to the end, as in Markdown.
func fencedRanges(s string) ranges {
	var out ranges
	var fence byte
	var width, start int
	for at := 0; at < len(s); {
		end := strings.IndexByte(s[at:], '\n')
		if end < 0 {
			end = len(s)
		} else {
			end += at + 1
		}
		line := strings.TrimRight(s[at:end], "\r\n")
		indent := len(line) - len(strings.TrimLeft(line, " "))
		rest := line[indent:]
		n := 0
		if indent <= 3 && rest != "" && (rest[0] == '`' || rest[0] == '~') {
			for n < len(rest) && rest[n] == rest[0] {
				n++
			}
		}
		switch {
		case fence == 0 && n >= 3 && !(rest[0] == '`' && strings.Contains(rest[n:], "`")):
			fence, width, start = rest[0], n, at
		case fence != 0 && n >= width && rest[0] == fence && strings.TrimSpace(rest[n:]) == "":
			out = append(out, [2]int{start, end})
			fence = 0
		}
		at = end
	}
	if fence != 0 {
		out = append(out, [2]int{start, len(s)})
	}
	return out
}

// inlineCode are the code spans of s between from and to: what is
// between two runs of as many backticks.
func inlineCode(s string, from, to int) ranges {
	// The runs of backticks, each with the next run as wide: a run that
	// closes none is passed by once, not looked past to the end for each.
	type run struct{ at, end, next int }
	var runs []run
	for at := from; at < to; {
		i := strings.IndexByte(s[at:to], '`')
		if i < 0 {
			break
		}
		at += i
		end := at
		for end < to && s[end] == '`' {
			end++
		}
		runs = append(runs, run{at: at, end: end, next: -1})
		at = end
	}
	if len(runs) < 2 {
		return nil
	}
	after := map[int]int{}
	for i := len(runs) - 1; i >= 0; i-- {
		w := runs[i].end - runs[i].at
		if j, ok := after[w]; ok {
			runs[i].next = j
		}
		after[w] = i
	}
	var out ranges
	for i := 0; i < len(runs); {
		j := runs[i].next
		if j < 0 {
			i++
			continue
		}
		out = append(out, [2]int{runs[i].at, runs[j].end})
		i = j + 1
	}
	return out
}

// mdDest is where a Markdown link leads, written without angle
// brackets: it may hold parentheses that pair, as the address of many a
// picture does. mdTitle is the title that may follow it.
const (
	mdDest  = `(?:[^()\s]|\([^()\s]*\))*`
	mdTitle = `(?:\s+(?:"[^"]*"|'[^']*'|\([^()]*\)))?\s*`
)

var (
	preTag      = regexp.MustCompile(`(?is)<pre(?:\s[^>]*)?>(.*?)</pre\s*>`)
	indentedTag = regexp.MustCompile(`(?m)^[ \t]+(</?[a-zA-Z])`)
	tableTag    = regexp.MustCompile(`(?is)<table(?:\s[^>]*)?>(.*?)</table\s*>`)
	rowTag      = regexp.MustCompile(`(?is)<tr(?:\s[^>]*)?>(.*?)</tr\s*>`)
	cellTag     = regexp.MustCompile(`(?is)<t[hd](?:\s[^>]*)?>(.*?)</t[hd]\s*>`)
	cellBlock   = regexp.MustCompile(`(?i)</?(p|div|center)(\s[^>]*)?>`)
	linkedImage = regexp.MustCompile(`\[!\[([^\]]*)\]\(\s*(?:<[^>\n]*>|` + mdDest + `)` + mdTitle + `\)\]\(\s*(?:<([^>\n]*)>|(` + mdDest + `))` + mdTitle + `\)`)
	mdImage     = regexp.MustCompile(`!\[([^\]]*)\]\(\s*(?:<([^>\n]*)>|(` + mdDest + `))` + mdTitle + `\)`)
	anchorTag   = regexp.MustCompile(`(?is)<a\s[^>]*?href\s*=\s*("[^"]*"|'[^']*')[^>]*>(.*?)</a\s*>`)
	imgTag      = regexp.MustCompile(`(?is)<img(\s[^>]*)?/?>`)
	attrTag     = regexp.MustCompile(`(?is)\s(src|alt)\s*=\s*("[^"]*"|'[^']*')`)
	headingTag  = regexp.MustCompile(`(?is)<h([1-6])(?:\s[^>]*)?>(.*?)</h[1-6]\s*>`)
	ruleTag     = regexp.MustCompile(`(?i)<hr(\s[^>]*)?/?>`)
	itemTag     = regexp.MustCompile(`(?i)\s*<li(\s[^>]*)?>`)
	loneBreak   = regexp.MustCompile(`(?im)^[ \t]*<br\s*/?>[ \t]*$`)
	blockTags   = regexp.MustCompile(`(?i)</?(p|div|center|section|article|blockquote|figure|figcaption|header|footer|summary|ul|ol|details)(\s[^>]*)?>`)
	droppedTags = regexp.MustCompile(`(?i)</?(span|font|source|picture|li|tbody|thead|a)(\s[^>]*)?/?>`)
	blankLines  = regexp.MustCompile(`\n{3,}`)
)

// tidyComment makes Markdown of the HTML in s, the code in it left as
// written.
func tidyComment(s, image string) string {
	var b strings.Builder
	at := 0
	for _, f := range append(fencedRanges(s), [2]int{len(s), len(s)}) {
		b.WriteString(tidyText(s[at:f[0]], image))
		b.WriteString(s[f[0]:f[1]])
		at = f[1]
	}
	return b.String()
}

// held marks what tidyText leaves as written while it changes the text
// around it. A comment shown has no control characters (shownBody), so
// none of it reads as a mark.
const held = "\x00"

func tidyText(s, image string) string {
	if !strings.ContainsAny(s, "<!") {
		return s
	}
	var kept []string
	keep := func(text string) string {
		kept = append(kept, text)
		return held + strconv.Itoa(len(kept)-1) + held
	}
	s = strings.ReplaceAll(s, held, "")
	var b strings.Builder
	at := 0
	for _, r := range inlineCode(s, 0, len(s)) {
		b.WriteString(s[at:r[0]])
		b.WriteString(keep(s[r[0]:r[1]]))
		at = r[1]
	}
	b.WriteString(s[at:])
	s = b.String()

	s = preTag.ReplaceAllStringFunc(s, func(m string) string {
		code := html.UnescapeString(anyTag.ReplaceAllString(preTag.FindStringSubmatch(m)[1], ""))
		fence := "```"
		for strings.Contains(code, fence) {
			fence += "`"
		}
		return "\n\n" + keep(fence+"\n"+strings.Trim(code, "\n")+"\n"+fence) + "\n\n"
	})
	// Markup indented as its author nests it would read as code once it
	// is Markdown.
	s = indentedTag.ReplaceAllString(s, "$1")
	s = tableTag.ReplaceAllStringFunc(s, func(m string) string { return htmlTable(tableTag.FindStringSubmatch(m)[1], m) })
	named := func(alt string) string {
		if alt = strings.TrimSpace(alt); alt != "" {
			return alt
		}
		return image
	}
	// The address is in angle brackets, or without them.
	s = linkedImage.ReplaceAllStringFunc(s, func(m string) string {
		p := linkedImage.FindStringSubmatch(m)
		return mdLink(named(p[1]), p[2]+p[3])
	})
	s = mdImage.ReplaceAllStringFunc(s, func(m string) string {
		p := mdImage.FindStringSubmatch(m)
		return mdLink(named(p[1]), p[2]+p[3])
	})
	picture := func(tag string) (alt, src string) {
		for _, a := range attrTag.FindAllStringSubmatch(tag, -1) {
			v := html.UnescapeString(a[2][1 : len(a[2])-1])
			if strings.EqualFold(a[1], "src") {
				src = v
			} else {
				alt = v
			}
		}
		return named(alt), src
	}
	s = anchorTag.ReplaceAllStringFunc(s, func(m string) string {
		p := anchorTag.FindStringSubmatch(m)
		// A picture that is a link goes where the link does.
		text := imgTag.ReplaceAllStringFunc(p[2], func(tag string) string {
			alt, _ := picture(tag)
			return alt
		})
		text = strings.TrimSpace(spaces.ReplaceAllString(text, " "))
		dest := html.UnescapeString(p[1][1 : len(p[1])-1])
		if text == "" {
			text = dest
		}
		return mdLink(text, dest)
	})
	s = imgTag.ReplaceAllStringFunc(s, func(tag string) string {
		alt, src := picture(tag)
		if src == "" {
			return alt
		}
		return mdLink(alt, src)
	})
	s = headingTag.ReplaceAllStringFunc(s, func(m string) string {
		p := headingTag.FindStringSubmatch(m)
		n, _ := strconv.Atoi(p[1])
		return "\n\n" + strings.Repeat("#", n) + " " + strings.TrimSpace(spaces.ReplaceAllString(p[2], " ")) + "\n\n"
	})
	s = ruleTag.ReplaceAllString(s, "\n\n---\n\n")
	s = itemTag.ReplaceAllString(s, "\n- ")
	s = loneBreak.ReplaceAllString(s, "")
	s = blockTags.ReplaceAllString(s, "\n\n")
	s = droppedTags.ReplaceAllString(s, "")
	s = blankLines.ReplaceAllString(s, "\n\n")
	return putBack(s, kept)
}

// putBack writes s with what tidyText held back in place of each mark,
// in one pass: one replace over the whole text for each mark would take
// a time that grows with their number times the text's length. What is
// held may hold marks of what was held before it (code in a <pre>).
func putBack(s string, kept []string) string {
	if len(kept) == 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	var put func(s string, below int)
	put = func(s string, below int) {
		for {
			i := strings.Index(s, held)
			if i < 0 {
				b.WriteString(s)
				return
			}
			b.WriteString(s[:i])
			rest := s[i+len(held):]
			j := strings.Index(rest, held)
			n, err := strconv.Atoi(rest[:max(j, 0)])
			if j < 0 || err != nil || n < 0 || n >= below {
				// Not a mark: the text had none of its own (tidyText takes
				// them out), but a rewrite may have split one.
				b.WriteString(held)
				s = rest
				continue
			}
			put(kept[n], n)
			s = rest[j+len(held):]
		}
	}
	put(s, len(kept))
	return b.String()
}

// mdLink is a Markdown link to dest named text.
func mdLink(text, dest string) string {
	text = strings.NewReplacer("[", `\[`, "]", `\]`, "\n", " ").Replace(text)
	if strings.ContainsAny(dest, " ()<>\n") {
		return "[" + text + "](<" + strings.NewReplacer("<", "%3C", ">", "%3E", "\n", "").Replace(dest) + ">)"
	}
	return "[" + text + "](" + dest + ")"
}

// htmlTable is the Markdown of an HTML table whose rows are inner; whole,
// the table as it was, when it has no rows to read.
func htmlTable(inner, whole string) string {
	var rows [][]string
	width := 0
	for _, r := range rowTag.FindAllStringSubmatch(inner, -1) {
		var cells []string
		for _, c := range cellTag.FindAllStringSubmatch(r[1], -1) {
			cell := cellBlock.ReplaceAllString(c[1], " ")
			cell = strings.ReplaceAll(strings.TrimSpace(spaces.ReplaceAllString(cell, " ")), "|", `\|`)
			cells = append(cells, cell)
		}
		if len(cells) > 0 {
			rows = append(rows, cells)
			width = max(width, len(cells))
		}
	}
	if len(rows) == 0 {
		return whole
	}
	var b strings.Builder
	b.WriteString("\n\n")
	line := func(cells []string) {
		b.WriteString("|")
		for i := range width {
			cell := ""
			if i < len(cells) {
				cell = cells[i]
			}
			b.WriteString(" " + cell + " |")
		}
		b.WriteString("\n")
	}
	line(rows[0])
	b.WriteString("|" + strings.Repeat(" --- |", width) + "\n")
	for _, r := range rows[1:] {
		line(r)
	}
	b.WriteString("\n")
	return b.String()
}
