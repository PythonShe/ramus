// Package crashlog keeps reports of what went wrong in the app, on this
// machine only: a panic recovered in a repository's work, and a crash
// that ended the process, found at the next launch. A report says which
// Ramus, OS and git it was, the stack, and the last git commands run in
// the repository, with secrets and the home folder taken out. Nothing
// here sends anything anywhere.
package crashlog

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"ramus/internal/git/gitexe"
	"ramus/internal/git/lifetime"
	"ramus/internal/git/redact"
	"ramus/internal/git/run"
)

// Bounds on what the log keeps.
const (
	MaxReports  = 20
	maxCommands = 30
	// Of a crash's output, its head, the error and the goroutine that
	// crashed, and its end are kept.
	fatalHead = 64 << 10
	fatalTail = 192 << 10
)

// fatalFile receives what the runtime prints when the process dies.
const fatalFile = "fatal-output.txt"

// Log writes reports into one folder. It is safe for concurrent use; its
// methods that touch files are never called on the main thread.
type Log struct {
	dir string
	app string
	os  string
	// home is replaced by ~ in what a report says; fold has it found in
	// any case and with either slash, as on Windows.
	home string
	fold bool

	mu      sync.Mutex
	git     string
	journal *run.Journal
	// panics is when each panic value was last reported.
	panics map[string]time.Time
}

// New returns the log keeping its reports in dir, for the app at version.
func New(dir, version string) *Log {
	home, _ := os.UserHomeDir()
	return &Log{dir: dir, app: appVersion(version), os: osVersion(), home: home, fold: runtime.GOOS == "windows"}
}

// Dir is the folder of the reports.
func (l *Log) Dir() string { return l.dir }

// SetGit names the git the app runs and the journal of its commands, once
// they are known. A nil log keeps nothing.
func (l *Log) SetGit(g *gitexe.Git, j *run.Journal) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.git, l.journal = g.Version.Raw, j
}

// Report is what one report tells besides the app, OS and git.
type Report struct {
	Time time.Time
	// What happened, in a few words: "Panic in a repository's work".
	What  string
	Repo  string
	Value string
	Stack string
	// GitDir names the repository whose commands are listed.
	GitDir string
}

// Text is a report as it is written: secrets and the home folder out.
func (l *Log) Text(r Report) string {
	l.mu.Lock()
	git, j := l.git, l.journal
	l.mu.Unlock()
	var b strings.Builder
	fmt.Fprintf(&b, "Ramus crash report\n\n%s\nTime: %s\n", r.What, r.Time.Format("2006-01-02 15:04:05 -0700"))
	b.WriteString(l.Environment(git))
	if r.Repo != "" {
		fmt.Fprintf(&b, "Repository: %s\n", r.Repo)
	}
	if r.Value != "" {
		fmt.Fprintf(&b, "Panic: %s\n", r.Value)
	}
	fmt.Fprintf(&b, "\nStack:\n%s\n", strings.TrimRight(r.Stack, "\n"))
	if j != nil && r.GitDir != "" {
		recs := j.For(r.GitDir)
		recs = recs[max(0, len(recs)-maxCommands):]
		if len(recs) > 0 {
			b.WriteString("\nLast git commands in the repository, oldest first:\n")
			for _, c := range recs {
				fmt.Fprintf(&b, "%s  %s  %s\n", c.Started.Format("15:04:05.000"), outcome(c), c.Short)
			}
		}
	}
	return l.clean(b.String())
}

// Environment names the app, the OS and git, a line each.
func (l *Log) Environment(git string) string {
	if git == "" {
		l.mu.Lock()
		git = l.git
		l.mu.Unlock()
	}
	if git == "" {
		git = "not found yet"
	}
	return fmt.Sprintf("Ramus: %s\nOS: %s\nGit: %s\n", l.app, l.os, git)
}

func outcome(c run.Record) string {
	switch {
	case c.Long:
		return "started"
	case c.Stopped:
		return "stopped"
	case c.Err != "" && c.Exit >= 0:
		return fmt.Sprintf("exit %d after %v", c.Exit, c.Duration.Round(time.Millisecond))
	case c.Err != "":
		return "failed: " + c.Err
	}
	return "ok after " + c.Duration.Round(time.Millisecond).String()
}

// clean takes out what must not be kept: escape sequences, secrets, and
// the user's home folder, which names them.
func (l *Log) clean(s string) string {
	s = redact.Shown(s)
	if l.home == "" || l.home == "/" {
		return s
	}
	if !l.fold {
		return strings.ReplaceAll(s, l.home, "~")
	}
	// Windows: git and Go print the folder with forward slashes too, and
	// with the drive letter or the name in another case.
	spellings := regexp.QuoteMeta(l.home)
	if slash := strings.ReplaceAll(l.home, `\`, "/"); slash != l.home {
		spellings += "|" + regexp.QuoteMeta(slash)
	}
	return regexp.MustCompile("(?i)"+spellings).ReplaceAllLiteralString(s, "~")
}

// samePanicGap is how long a panic with the same value as one reported
// is not reported again: work that reruns on every change of the files
// would otherwise write a report many times a second.
const samePanicGap = time.Minute

// Panic writes a report of a panic recovered in the work of the
// repository at path (git directory gitDir), and returns its file; ""
// when one with the same value was reported less than samePanicGap ago.
func (l *Log) Panic(path, gitDir string, p lifetime.Panic) (string, error) {
	now := time.Now()
	l.mu.Lock()
	if last, ok := l.panics[p.Value]; ok && now.Sub(last) < samePanicGap {
		l.mu.Unlock()
		return "", nil
	}
	if l.panics == nil {
		l.panics = map[string]time.Time{}
	}
	for v, at := range l.panics {
		if now.Sub(at) >= samePanicGap {
			delete(l.panics, v)
		}
	}
	l.panics[p.Value] = now
	l.mu.Unlock()
	return l.write(Report{Time: now, What: "A panic was recovered in a repository's work; the app went on.",
		Repo: path, Value: p.Value, Stack: p.Stack, GitDir: gitDir})
}

func (l *Log) write(r Report) (string, error) {
	if err := os.MkdirAll(l.dir, 0o700); err != nil {
		return "", err
	}
	// Two reports in the same millisecond, as of one bug hit by two
	// repositories at once, each keep a file.
	base := filepath.Join(l.dir, "crash-"+r.Time.Format("2006-01-02T15-04-05.000"))
	name := base + ".txt"
	var f *os.File
	for i := 2; ; i++ {
		var err error
		f, err = os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) || i > 100 {
			return "", err
		}
		name = fmt.Sprintf("%s-%d.txt", base, i)
	}
	_, err := f.WriteString(l.Text(r))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	l.prune()
	return name, nil
}

// Reports lists the reports kept, newest first.
func (l *Log) Reports() ([]string, error) {
	names, err := filepath.Glob(filepath.Join(l.dir, "crash-*.txt"))
	if err != nil {
		return nil, err
	}
	slices.Sort(names)
	slices.Reverse(names)
	return names, nil
}

func (l *Log) prune() {
	names, _ := l.Reports()
	for _, n := range names[min(len(names), MaxReports):] {
		os.Remove(n)
	}
}

// CatchFatal has the runtime also print a crash that ends the process
// into the log's folder. What an earlier crash printed there is first set
// aside, for ReportFatal to turn into a report. It costs a rename and an
// open, so that it may run first thing at launch, before anything else
// that could crash; the report, which reads and writes more, comes after.
func (l *Log) CatchFatal() error {
	if err := os.MkdirAll(l.dir, 0o700); err != nil {
		return err
	}
	name := filepath.Join(l.dir, fatalFile)
	if st, err := os.Stat(name); err == nil && st.Size() > 0 {
		pending := filepath.Join(l.dir, fmt.Sprintf("fatal-%d%s", time.Now().UnixNano(), pendingExt))
		if err := os.Rename(name, pending); err != nil {
			// Not caught this time, rather than the last crash lost.
			return err
		}
	}
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	return debug.SetCrashOutput(f, debug.CrashOptions{})
}

// pendingExt names what a crash printed, set aside by CatchFatal.
const pendingExt = ".pending"

// ReportFatal turns what earlier crashes printed, which CatchFatal set
// aside, into reports, and returns their files, oldest first.
func (l *Log) ReportFatal() ([]string, error) {
	names, err := filepath.Glob(filepath.Join(l.dir, "fatal-*"+pendingExt))
	if err != nil {
		return nil, err
	}
	slices.Sort(names)
	var reports []string
	var errs []error
	for _, name := range names {
		report, err := l.reportFatal(name)
		if report != "" {
			reports = append(reports, report)
		}
		if err == nil {
			err = os.Remove(name)
		}
		errs = append(errs, err)
	}
	return reports, errors.Join(errs...)
}

func (l *Log) reportFatal(name string) (string, error) {
	f, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	out, err := readEnds(f, st.Size(), fatalHead, fatalTail)
	if err != nil || strings.TrimSpace(out) == "" {
		return "", err
	}
	return l.write(Report{Time: st.ModTime(), What: "Ramus stopped on a fatal error.", Stack: out})
}

// stopFatal undoes CatchFatal, for tests.
func (l *Log) stopFatal() { debug.SetCrashOutput(nil, debug.CrashOptions{}) }

// readEnds reads f, of size bytes, whole when it holds at most head+tail
// bytes, else its first head bytes and its last tail, each cut at a line,
// with a line between them that says how much was left out. The runtime
// prints the error and the goroutine that crashed first, and may then
// print every other goroutine.
func readEnds(f *os.File, size, head, tail int64) (string, error) {
	if size <= head+tail {
		buf := make([]byte, size)
		n, err := f.ReadAt(buf, 0)
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return string(buf[:n]), nil
	}
	first := make([]byte, head)
	if _, err := f.ReadAt(first, 0); err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	last := make([]byte, tail)
	if _, err := f.ReadAt(last, size-tail); err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if i := bytes.LastIndexByte(first, '\n'); i >= 0 {
		first = first[:i+1]
	}
	if i := bytes.IndexByte(last, '\n'); i >= 0 {
		last = last[i+1:]
	}
	left := size - int64(len(first)) - int64(len(last))
	return fmt.Sprintf("%s\n... %d bytes left out ...\n\n%s", first, left, last), nil
}

// appVersion is the version packaged, or the commit built from.
func appVersion(packaged string) string {
	v := packaged
	if v == "" {
		v = "development build"
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		var rev, dirty string
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value[:min(12, len(s.Value))]
			case "vcs.modified":
				if s.Value == "true" {
					dirty = ", modified"
				}
			}
		}
		if rev != "" {
			v += " (" + rev + dirty + ")"
		}
		v += ", " + info.GoVersion
	}
	return v
}

// osVersion names the OS as its own files do, without running anything.
func osVersion() string {
	arch := runtime.GOOS + "/" + runtime.GOARCH
	switch runtime.GOOS {
	case "darwin":
		if data, err := os.ReadFile("/System/Library/CoreServices/SystemVersion.plist"); err == nil {
			if v := plistString(string(data), "ProductVersion"); v != "" {
				return "macOS " + v + " (" + arch + ")"
			}
		}
	case "linux":
		if data, err := os.ReadFile("/etc/os-release"); err == nil {
			for line := range strings.SplitSeq(string(data), "\n") {
				if v, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
					return strings.Trim(v, `"`) + " (" + arch + ")"
				}
			}
		}
	}
	return arch
}

// plistString is the string value of key in an XML property list.
func plistString(plist, key string) string {
	_, rest, ok := strings.Cut(plist, "<key>"+key+"</key>")
	if !ok {
		return ""
	}
	_, rest, ok = strings.Cut(rest, "<string>")
	if !ok {
		return ""
	}
	v, _, _ := strings.Cut(rest, "</string>")
	return strings.TrimSpace(v)
}
