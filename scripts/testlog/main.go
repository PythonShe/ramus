// Command testlog reads the events of go test -json, keeps them in a file
// and prints what go test prints without -json: each package's result,
// and the output of the tests that failed. Later, it lists the tests the
// kept events say were skipped, each with its reason, or names those
// that failed.
//
//	go test -json ./... | go run ./scripts/testlog -save tests.json
//	go run ./scripts/testlog -skips tests.json ...
//	go run ./scripts/testlog -failed "go test" tests.json ...
//
// scripts/check.sh runs its tests through it, and names the tests that
// failed on its last lines, so that a long log need not be searched.
// When RAMUS_TEST_JSON names a folder, the events are kept there, and CI
// lists the skipped tests from them: a test that skips where it should
// run passes without testing anything, which only such a list shows,
// and the tests run once.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
)

// event is one line of go test -json (go doc test2json).
type event struct {
	Action     string
	Package    string
	ImportPath string // of a build-output event
	Test       string
	Output     string
	// FailedBuild names, on a package's fail, the build that failed.
	FailedBuild string
}

func main() {
	save := flag.String("save", "", "the file to keep the events read from standard input in")
	skips := flag.Bool("skips", false, "list the tests skipped in the files named, each with its reason")
	failed := flag.String("failed", "", "name, as check.sh's step of this name, each test and package that failed in the files named")
	flag.Parse()
	var err error
	switch {
	case *skips:
		err = listSkips(os.Stdout, flag.Args())
	case *failed != "":
		err = listFailures(os.Stdout, *failed, flag.Args())
	default:
		err = render(os.Stdin, os.Stdout, *save)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "testlog:", err)
		os.Exit(1)
	}
}

// render prints the events of in as go test prints without -json, and
// keeps each in save, when set. A line that is not an event, as go
// test's own errors, is printed as it is.
func render(in io.Reader, out io.Writer, save string) error {
	var kept io.Writer = io.Discard
	if save != "" {
		f, err := os.Create(save)
		if err != nil {
			return err
		}
		defer f.Close()
		w := bufio.NewWriter(f)
		defer w.Flush()
		kept = w
	}
	// The output of each test running, printed should it fail.
	running := map[[2]string][]string{}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for sc.Scan() {
		line := sc.Bytes()
		fmt.Fprintf(kept, "%s\n", line)
		var e event
		if json.Unmarshal(line, &e) != nil || e.Action == "" {
			fmt.Fprintf(out, "%s\n", line)
			continue
		}
		key := [2]string{e.Package, e.Test}
		switch {
		case e.Action == "build-output":
			fmt.Fprint(out, e.Output)
		case e.Test == "":
			// The package's own: its result, and a panic or a timeout
			// outside any test. PASS is printed only by go test -v.
			if e.Action == "output" && e.Output != "PASS\n" {
				fmt.Fprint(out, e.Output)
			}
		case e.Action == "output":
			running[key] = append(running[key], e.Output)
		case e.Action == "fail":
			// The test's "--- FAIL" line, which comes last, first, as
			// go test prints it.
			lines := running[key]
			for _, head := range []bool{true, false} {
				for _, l := range lines {
					if !strings.HasPrefix(l, "=== ") && strings.HasPrefix(l, "--- ") == head {
						fmt.Fprint(out, l)
					}
				}
			}
			delete(running, key)
		case e.Action == "pass" || e.Action == "skip":
			delete(running, key)
		}
	}
	return sc.Err()
}

// listSkips prints, for the events in files, each test skipped and why,
// by package, and how many.
func listSkips(out io.Writer, files []string) error {
	type skip struct{ pkg, test, why string }
	var all []skip
	for _, name := range files {
		f, err := os.Open(name)
		if err != nil {
			return err
		}
		output := map[[2]string][]string{}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
		for sc.Scan() {
			var e event
			if json.Unmarshal(sc.Bytes(), &e) != nil || e.Test == "" {
				continue
			}
			key := [2]string{e.Package, e.Test}
			switch e.Action {
			case "output":
				output[key] = append(output[key], e.Output)
			case "skip":
				var why []string
				for _, l := range output[key] {
					l = strings.TrimSpace(l)
					if l != "" && !strings.HasPrefix(l, "=== ") && !strings.HasPrefix(l, "--- SKIP") {
						why = append(why, l)
					}
				}
				all = append(all, skip{e.Package, e.Test, strings.Join(why, " ")})
				delete(output, key)
			case "pass", "fail":
				delete(output, key)
			}
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	slices.SortFunc(all, func(a, b skip) int {
		return strings.Compare(a.pkg+"\x00"+a.test, b.pkg+"\x00"+b.test)
	})
	pkg := ""
	for _, s := range all {
		if s.pkg != pkg {
			pkg = s.pkg
			fmt.Fprintln(out, pkg)
		}
		why := s.why
		if why == "" {
			why = "(no reason given)"
		}
		fmt.Fprintf(out, "  %s: %s\n", s.test, why)
	}
	fmt.Fprintf(out, "%d skipped\n", len(all))
	return nil
}

// failedTest is a test of a package that failed, as listFailures reads
// it: how it ended, "" for a pass or a skip.
type failedTest struct {
	name, how string
	ended     bool
}

// listFailures prints a line for each test that failed in the events of
// files, by package and name, and how: failed, panicked, had a data race,
// or ran past the time limit; and for each package that failed without a
// test to blame: did not build, or had a data race outside any test. A
// subtest is named rather than the test it runs in. Each line starts
// "check.sh: step: ", for the gate's last lines.
func listFailures(out io.Writer, step string, files []string) error {
	type pkg struct {
		tests  []*failedTest
		byName map[string]*failedTest
		output []string
	}
	say := func(s string) { fmt.Fprintf(out, "check.sh: %s: %s\n", step, s) }
	for _, name := range files {
		f, err := os.Open(name)
		if err != nil {
			return err
		}
		pkgs := map[string]*pkg{}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
		for sc.Scan() {
			var e event
			if json.Unmarshal(sc.Bytes(), &e) != nil || e.Package == "" {
				continue
			}
			p := pkgs[e.Package]
			if p == nil {
				p = &pkg{byName: map[string]*failedTest{}}
				pkgs[e.Package] = p
			}
			var t *failedTest
			if e.Test != "" {
				if t = p.byName[e.Test]; t == nil {
					t = &failedTest{name: e.Test}
					p.byName[e.Test] = t
					p.tests = append(p.tests, t)
				}
			}
			switch {
			case e.Action == "output":
				p.output = append(p.output, e.Output)
				switch {
				case t == nil:
				case strings.Contains(e.Output, "race detected during execution of test"):
					t.how = "had a data race"
				case strings.HasPrefix(e.Output, "panic: ") && !strings.HasPrefix(e.Output, "panic: test timed out") && t.how == "":
					t.how = "panicked"
				}
			case t != nil && e.Action == "fail":
				t.ended = true
				if t.how == "" {
					t.how = "failed"
				}
			case t != nil && (e.Action == "pass" || e.Action == "skip"):
				t.ended, t.how = true, ""
			case t == nil && e.Action == "fail":
				failedPackage(say, e, p.tests, p.output)
				delete(pkgs, e.Package)
			case t == nil && (e.Action == "pass" || e.Action == "skip"):
				delete(pkgs, e.Package)
			}
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

// failedPackage names what failed in package e.Package, given its tests
// in the order they started and all its output.
func failedPackage(say func(string), e event, tests []*failedTest, output []string) {
	if e.FailedBuild != "" {
		say(e.Package + " did not build")
		return
	}
	timedOut, raced, panicked := false, false, false
	for _, l := range output {
		timedOut = timedOut || strings.HasPrefix(l, "panic: test timed out")
		raced = raced || strings.HasPrefix(l, "testing: race detected outside of test execution") ||
			strings.HasPrefix(l, "Found ") && strings.Contains(l, " data race")
		panicked = panicked || strings.HasPrefix(l, "panic: ")
	}
	named := false
	for _, t := range tests {
		how := t.how
		if !t.ended {
			// Still running when the package stopped: past the time
			// limit, or in a crash.
			how = "did not finish"
			if timedOut {
				how = "ran past the time limit"
			}
		}
		if how == "" || slices.ContainsFunc(tests, func(u *failedTest) bool {
			return u.how != "" && strings.HasPrefix(u.name, t.name+"/")
		}) {
			continue
		}
		say(t.name + " in " + e.Package + " " + how)
		named = true
	}
	switch {
	case raced:
		say(e.Package + " had a data race outside any test")
	case named:
	case panicked:
		say(e.Package + " failed in a panic, naming no test")
	default:
		say(e.Package + " failed, naming no test")
	}
}
