// Package crashlog keeps reports of what went wrong in the app, on this
// machine only: a panic recovered in a repository's work, and a crash
// that ended the process, found at the next launch. A report says which
// Ramus, OS and git it was, the stack, and the last git commands run in
// the repository, with secrets and the home folder taken out. Nothing
// here sends anything anywhere.
package crashlog

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	maxFatal    = 256 << 10 // of a crash's output, its end kept
)

// fatalFile receives what the runtime prints when the process dies.
const fatalFile = "fatal-output.txt"

// Log writes reports into one folder. It is safe for concurrent use; its
// methods that touch files are never called on the main thread.
type Log struct {
	dir string
	app string
	os  string
	// home is replaced by ~ in what a report says.
	home string

	mu      sync.Mutex
	git     string
	journal *run.Journal
}

// New returns the log keeping its reports in dir, for the app at version.
func New(dir, version string) *Log {
	home, _ := os.UserHomeDir()
	return &Log{dir: dir, app: appVersion(version), os: osVersion(), home: home}
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
	if l.home != "" && l.home != "/" {
		s = strings.ReplaceAll(s, l.home, "~")
	}
	return s
}

// Panic writes a report of a panic recovered in the work of the
// repository at path (git directory gitDir), and returns its file.
func (l *Log) Panic(path, gitDir string, p lifetime.Panic) (string, error) {
	return l.write(Report{Time: time.Now(), What: "A panic was recovered in a repository's work; the app went on.",
		Repo: path, Value: p.Value, Stack: p.Stack, GitDir: gitDir})
}

func (l *Log) write(r Report) (string, error) {
	if err := os.MkdirAll(l.dir, 0o700); err != nil {
		return "", err
	}
	name := filepath.Join(l.dir, "crash-"+r.Time.Format("2006-01-02T15-04-05.000")+".txt")
	if err := os.WriteFile(name, []byte(l.Text(r)), 0o600); err != nil {
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
// into the log's folder, and first turns what an earlier one printed
// there into a report. It returns that report's file, "" when there was
// none.
func (l *Log) CatchFatal() (string, error) {
	if err := os.MkdirAll(l.dir, 0o700); err != nil {
		return "", err
	}
	f, err := os.OpenFile(filepath.Join(l.dir, fatalFile), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var report string
	out, err := readEnd(f, maxFatal)
	if err == nil && len(strings.TrimSpace(out)) > 0 {
		st, _ := f.Stat()
		when := time.Now()
		if st != nil {
			when = st.ModTime()
		}
		report, err = l.write(Report{Time: when, What: "Ramus stopped on a fatal error.", Stack: out})
	}
	if err != nil {
		return "", err
	}
	if err := f.Truncate(0); err != nil {
		return report, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return report, err
	}
	return report, debug.SetCrashOutput(f, debug.CrashOptions{})
}

// stopFatal undoes CatchFatal, for tests.
func (l *Log) stopFatal() { debug.SetCrashOutput(nil, debug.CrashOptions{}) }

// readEnd reads the last max bytes of f.
func readEnd(f *os.File, max int64) (string, error) {
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	from := st.Size() - max
	if from < 0 {
		from = 0
	}
	buf := make([]byte, st.Size()-from)
	if _, err := f.ReadAt(buf, from); err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return string(buf), nil
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
