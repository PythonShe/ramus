// Command monorepo builds a large synthetic git repository for benchmarks:
// a few hundred thousand files in many languages and scripts, awkward file
// names and encodings, files of 100k to 1M lines, and a history with big
// commits, renames and a merge. Everything derives from one seed, so two runs
// give the same repository.
//
//	go run ./bench/monorepo -out DIR             build the repository
//	go run ./bench/monorepo -out DIR -dirty 500  change 500 files in its work tree
//
// The bench's status-monorepo case reads it as monorepo.noindex in the
// fixtures folder; the suffix keeps Spotlight out of it on macOS.
package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

type file struct {
	path    string
	kind    int
	seed    uint64
	lines   int
	ver     int
	sc      int
	mode    string
	deleted bool
}

type builder struct {
	r     *rng
	files []*file
	taken map[string]bool
	w     *bufio.Writer
	mark  int
	when  int64
	buf   bytes.Buffer
}

func main() {
	out := flag.String("out", "", "directory to create the repository in (must not exist)")
	n := flag.Int("files", 390000, "tracked files in the first commit")
	seed := flag.Uint64("seed", 20261113, "seed for everything")
	ignored := flag.Int("ignored", 25000, "ignored files written after checkout")
	dirtyN := flag.Int("dirty", 0, "change this many files in an existing repository instead of building one")
	flag.Parse()
	if *out == "" {
		fatal("need -out")
	}
	if *dirtyN > 0 {
		dirty(*out, *dirtyN, *seed)
		return
	}
	if _, err := os.Stat(*out); err == nil {
		fatal(*out + " already exists; remove it first")
	}
	check(os.MkdirAll(*out, 0o755))
	git(*out, nil, "init", "-q", "-b", "main")
	git(*out, nil, "config", "user.name", "Fixture")
	git(*out, nil, "config", "user.email", "fixture@example.invalid")

	imp := exec.Command("git", "-C", *out, "fast-import", "--quiet")
	stdin, err := imp.StdinPipe()
	check(err)
	imp.Stderr = os.Stderr
	check(imp.Start())

	b := &builder{r: &rng{s: *seed, sc: scMixed}, taken: map[string]bool{}, w: bufio.NewWriterSize(stdin, 1<<20), when: 1546300800}
	b.history(*n)
	check(b.w.Flush())
	check(stdin.Close())
	check(imp.Wait())

	progress("checking out")
	git(*out, nil, "reset", "-q", "--hard")
	progress("writing %d ignored files", *ignored)
	writeIgnored(*out, *ignored, *seed)
	progress("done")
}

// history writes the whole fast-import stream.
func (b *builder) history(n int) {
	sp := specials()
	b.plan(n - len(sp))
	progress("planned %d files", len(b.files)+len(sp))

	first := b.commit("refs/heads/main", "chore: initial import", 0, 0)
	for i, f := range b.files {
		b.modify(f.mode, f.path, b.content(f))
		if i%50000 == 0 && i > 0 {
			progress("  %d files written", i)
		}
	}
	for _, s := range sp {
		b.modify(s.mode, s.path, s.data(0))
	}
	b.tag("v0.1.0", first, "First import.\n")
	progress("first commit written")

	for i := 0; i < 24; i++ {
		b.ordinary("refs/heads/main", 0, nil)
	}
	fork := b.mark

	// A feature branch that is merged back, and one that is left open.
	var touched []*file
	feat := 0
	for i := 0; i < 6; i++ {
		from := 0
		if i == 0 {
			from = fork
		}
		feat = b.ordinary("refs/heads/feature/图表-graph", from, &touched)
	}
	for i := 0; i < 10; i++ {
		b.ordinary("refs/heads/main", 0, nil)
	}
	b.commit("refs/heads/main", "Merge branch 'feature/图表-graph'", 0, feat)
	for _, f := range touched {
		if !f.deleted {
			b.modify(f.mode, f.path, b.content(f))
		}
	}
	wipFrom := b.mark
	for i := 0; i < 3; i++ {
		from := 0
		if i == 0 {
			from = wipFrom
		}
		b.ordinary("refs/heads/wip/ünmerged-experiment", from, nil)
	}
	b.lightTag("v1.0.0", wipFrom)

	// One commit that touches 10,000 files.
	b.commit("refs/heads/main", "perf(bindings): regenerate every binding\n\nTouches 10,000 files in one commit.", 0, 0)
	bumped := map[*file]bool{}
	for i := 0; i < 10000; {
		f := b.files[b.r.n(len(b.files))]
		if f.deleted || bumped[f] {
			continue
		}
		bumped[f] = true
		f.ver++
		b.modify(f.mode, f.path, b.content(f))
		i++
	}

	// 2,000 renames, half of them edited as well.
	b.commit("refs/heads/main", "refactor: rename 2,000 modules\n\nHalf are pure renames; half change content too.", 0, 0)
	for i := 0; i < 2000; {
		f := b.files[b.r.n(len(b.files))]
		if f.deleted || strings.HasPrefix(path.Base(f.path), "renamed_") {
			continue
		}
		to := path.Join(path.Dir(f.path), "renamed_"+path.Base(f.path))
		if b.taken[strings.ToLower(to)] {
			continue
		}
		b.taken[strings.ToLower(to)] = true
		fmt.Fprintf(b.w, "R %s %s\n", quote(f.path), quote(to))
		f.path = to
		if i%2 == 0 {
			f.ver++
			b.modify(f.mode, f.path, b.content(f))
		}
		i++
	}

	// New versions of the huge files, for large diffs.
	b.commit("refs/heads/main", "chore(data): refresh generated data\n\nEvery file under edge/huge changes: scattered edits, inserted blocks and removed lines.", 0, 0)
	for _, s := range sp {
		if s.huge {
			b.modify(s.mode, s.path, s.data(1))
		}
	}
	b.lightTag("发布-2026", b.mark)

	for i := 0; i < 15; i++ {
		b.ordinary("refs/heads/main", 0, nil)
	}
	b.tag("v2.0.0-rc.1", b.mark, "Release candidate \U0001f680\n\n日本語のタグメッセージ。\n")
}

// ordinary writes one everyday commit: a few edits, sometimes an add or a
// delete. Files it touches are appended to touched when that is not nil.
func (b *builder) ordinary(ref string, from int, touched *[]*file) int {
	r := b.r
	m := b.commit(ref, message(r), from, 0)
	k := 1 + int(math.Pow(r.f(), 3)*40)
	for i := 0; i < k; {
		f := b.files[r.n(len(b.files))]
		if f.deleted {
			continue
		}
		i++
		if r.n(20) == 0 {
			f.deleted = true
			fmt.Fprintf(b.w, "D %s\n", fipath(f.path))
			continue
		}
		f.ver++
		b.modify(f.mode, f.path, b.content(f))
		if touched != nil {
			*touched = append(*touched, f)
		}
	}
	if r.n(4) == 0 {
		old := b.files[r.n(len(b.files))]
		f := b.newFile(path.Dir(old.path), kinds[old.kind].ext, old.sc)
		b.files = append(b.files, f)
		b.modify(f.mode, f.path, b.content(f))
		if touched != nil {
			*touched = append(*touched, f)
		}
	}
	return m
}

func (b *builder) commit(ref, msg string, from, merge int) int {
	a := authors[b.r.n(len(authors))]
	b.when += int64(600 + b.r.n(400000))
	b.mark++
	fmt.Fprintf(b.w, "commit %s\nmark :%d\n", ref, b.mark)
	fmt.Fprintf(b.w, "author %s <%s> %d %s\n", a.name, a.email, b.when-int64(b.r.n(7200)), a.tz)
	fmt.Fprintf(b.w, "committer %s <%s> %d %s\n", a.name, a.email, b.when, a.tz)
	fmt.Fprintf(b.w, "data %d\n%s\n", len(msg), msg)
	if from != 0 {
		fmt.Fprintf(b.w, "from :%d\n", from)
	}
	if merge != 0 {
		fmt.Fprintf(b.w, "merge :%d\n", merge)
	}
	return b.mark
}

func (b *builder) modify(mode, p string, data []byte) {
	fmt.Fprintf(b.w, "M %s inline %s\ndata %d\n", mode, fipath(p), len(data))
	b.w.Write(data)
	b.w.WriteByte('\n')
}

func (b *builder) tag(name string, mark int, msg string) {
	fmt.Fprintf(b.w, "tag %s\nfrom :%d\ntagger Fixture <fixture@example.invalid> %d +0000\ndata %d\n%s\n", name, mark, b.when, len(msg), msg)
}

func (b *builder) lightTag(name string, mark int) {
	fmt.Fprintf(b.w, "reset refs/tags/%s\nfrom :%d\n\n", name, mark)
}

// content returns a file's bytes at its current version. Later versions are
// the first one with a block inserted, a block removed and a few numbers
// changed per version, so consecutive versions give realistic diffs.
func (b *builder) content(f *file) []byte {
	k := &kinds[f.kind]
	b.buf.Reset()
	if k.binary {
		k.gen(&rng{s: f.seed + uint64(f.ver)*7919, sc: f.sc}, f.lines, &b.buf)
		return b.buf.Bytes()
	}
	k.gen(&rng{s: f.seed, sc: f.sc}, f.lines, &b.buf)
	if f.ver == 0 {
		return b.buf.Bytes()
	}
	lines := strings.SplitAfter(b.buf.String(), "\n")
	for v := 1; v <= f.ver; v++ {
		m := &rng{s: f.seed ^ uint64(v)*0x9e3779b97f4a7c15, sc: f.sc}
		var c bytes.Buffer
		k.chunk(m, &c)
		at := m.n(len(lines) + 1)
		next := make([]string, 0, len(lines)+16)
		next = append(next, lines[:at]...)
		next = append(next, strings.SplitAfter(c.String(), "\n")...)
		next = append(next, lines[at:]...)
		lines = next
		if len(lines) > 12 {
			at, d := m.n(len(lines)-6), 1+m.n(5)
			lines = append(lines[:at], lines[at+d:]...)
		}
		for i, e := 0, 1+m.n(3); i < e; i++ {
			j := m.n(len(lines))
			lines[j] = tweak(lines[j], m)
		}
	}
	return []byte(strings.Join(lines, ""))
}

// tweak changes the first digit on a line, for a one-word difference.
func tweak(s string, m *rng) string {
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			d := byte('0' + (int(s[i]-'0')+1+m.n(8))%10)
			return s[:i] + string(d) + s[i+1:]
		}
	}
	return s
}

// plan decides every path before anything is written.
func (b *builder) plan(n int) {
	r := b.r
	type dir struct {
		path string
		exts []string
	}
	byArea := make([][]dir, len(areas))
	total := 0
	for i, a := range areas {
		byArea[i] = []dir{{a.path, strings.Fields(a.exts)}}
		total += a.share
	}
	pickArea := func() int {
		x := r.n(total)
		for i, a := range areas {
			if x -= a.share; x < 0 {
				return i
			}
		}
		return 0
	}
	seen := map[string]bool{}
	for made := 0; made < n/24; {
		a := pickArea()
		parent := byArea[a][r.n(len(byArea[a]))]
		if strings.Count(parent.path, "/") > 9 {
			continue
		}
		name := r.pick(words)
		if r.n(3) == 0 {
			name += "-" + r.pick(words)
		}
		p := parent.path + "/" + name
		if seen[p] {
			continue
		}
		seen[p] = true
		byArea[a] = append(byArea[a], dir{p, parent.exts})
		made++
	}

	add := func(d string, ext string, sc int) {
		b.files = append(b.files, b.newFile(d, ext, sc))
	}
	// Three very large directories, as test-data folders are in real projects.
	giants := []struct {
		path string
		n    int
		exts []string
	}{
		{"third_party/webtests/layout", 20000, []string{"html", "html", "txt", "png"}},
		{"libs/icons/assets", 10000, []string{"png", "png", "png", "png", "json"}},
		{"services/api/generated", 5000, []string{"go", "ts", "proto"}},
	}
	for _, g := range giants {
		for i := 0; i < g.n; i++ {
			add(g.path, g.exts[r.n(len(g.exts))], scEnglish)
		}
	}
	// One folder of documents per script, with names in that script.
	for sc, s := range scripts {
		for i := 0; i < 150; i++ {
			ext := "md"
			if i%3 == 0 {
				ext = "txt"
			}
			add("docs/i18n/"+s.tag, ext, sc)
		}
	}
	for len(b.files) < n {
		a := pickArea()
		ds := byArea[a]
		d := ds[int(math.Pow(r.f(), 2.2)*float64(len(ds)))]
		ext := d.exts[int(math.Pow(r.f(), 1.6)*float64(len(d.exts)))]
		sc := scEnglish
		switch x := r.n(10); {
		case x == 0:
			sc = r.n(len(scripts))
		case x < 3:
			sc = scMixed
		}
		add(d.path, ext, sc)
	}
}

func (b *builder) newFile(dir, ext string, sc int) *file {
	r := b.r
	f := &file{kind: kindOf[ext], seed: r.u64(), sc: sc, mode: "100644"}
	f.lines = int(math.Exp(3.3 + 0.95*r.norm()))
	f.lines = max(3, min(f.lines, 4000))
	if ext == "sh" || r.n(100) == 0 {
		f.mode = "100755"
	}
	for try := 0; ; try++ {
		name := r.fileName(ext, sc, strings.HasPrefix(dir, "docs/i18n/"))
		if try > 2 {
			name = fmt.Sprintf("%d_%s", r.n(1000000), name)
		}
		p := dir + "/" + name
		if !b.taken[strings.ToLower(p)] {
			b.taken[strings.ToLower(p)] = true
			f.path = p
			return f
		}
	}
}

func writeIgnored(out string, n int, seed uint64) {
	r := &rng{s: seed + 1, sc: scEnglish}
	var buf bytes.Buffer
	for i := 0; i < n; i++ {
		root, ext := "node_modules", "js"
		if i%5 == 4 {
			root, ext = "build", "txt"
		}
		d := filepath.Join(out, root, r.pick(words)+"-"+r.pick(words), r.pick(words))
		check(os.MkdirAll(d, 0o755))
		buf.Reset()
		kinds[kindOf[ext]].gen(r, 5+r.n(60), &buf)
		check(os.WriteFile(filepath.Join(d, fmt.Sprintf("%s_%d.%s", r.pick(words), i, ext)), buf.Bytes(), 0o644))
	}
}

// dirty changes n tracked files in place: most are edited, one in ten gains
// an untracked neighbour and one in twenty is deleted.
func dirty(out string, n int, seed uint64) {
	raw, err := exec.Command("git", "-C", out, "ls-files", "-z").Output()
	check(err)
	paths := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
	r := &rng{s: seed + 2}
	for i := 0; i < n; i++ {
		p := filepath.Join(out, paths[r.n(len(paths))])
		st, err := os.Lstat(p)
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		switch {
		case i%20 == 19:
			os.Remove(p)
		case i%10 == 9:
			check(os.WriteFile(filepath.Join(filepath.Dir(p), fmt.Sprintf("untracked_%d.txt", i)), []byte("untracked\n"), 0o644))
		default:
			f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
			check(err)
			fmt.Fprintf(f, "\nchanged by fixturegen %d\n", i)
			f.Close()
		}
	}
}

func message(r *rng) string {
	types := []string{"feat", "fix", "refactor", "perf", "docs", "test", "chore", "build"}
	old := r.sc
	r.sc = scMixed
	defer func() { r.sc = old }()
	subject := fmt.Sprintf("%s(%s): %s", r.pick(types), r.pick(words), r.phrase())
	if r.n(2) == 0 {
		return subject
	}
	var b strings.Builder
	b.WriteString(subject + "\n")
	for i, p := 0, 1+r.n(3); i < p; i++ {
		b.WriteString("\n")
		switch r.n(3) {
		case 0:
			for j, k := 0, 2+r.n(4); j < k; j++ {
				b.WriteString("- " + r.phrase() + "\n")
			}
		case 1:
			for j, k := 0, 20+r.n(60); j < k; j++ {
				b.WriteString(r.pick(words) + " ")
			}
			b.WriteString(r.phrase() + "\n")
		default:
			b.WriteString(r.phrase() + " " + r.phrase() + "\n")
		}
	}
	if r.n(3) == 0 {
		a := authors[r.n(len(authors))]
		fmt.Fprintf(&b, "\nRefs: #%d\nReviewed-by: %s <%s>\n", r.n(9999), a.name, a.email)
	}
	return b.String()
}

// quote writes a path the way fast-import reads a quoted one.
func quote(p string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(p); i++ {
		switch c := p[i]; c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// fipath quotes only when fast-import would misread the bare path.
func fipath(p string) string {
	if strings.HasPrefix(p, `"`) || strings.Contains(p, "\n") {
		return quote(p)
	}
	return p
}

func git(dir string, stdin io.Reader, args ...string) {
	c := exec.Command("git", append([]string{"-C", dir}, args...)...)
	c.Stdin, c.Stdout, c.Stderr = stdin, os.Stdout, os.Stderr
	check(c.Run())
}

func progress(format string, a ...any) { fmt.Fprintf(os.Stderr, format+"\n", a...) }

func check(err error) {
	if err != nil {
		fatal(err.Error())
	}
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "fixturegen:", msg)
	os.Exit(1)
}
