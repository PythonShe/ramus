// Command testlog reads the events of go test -json, keeps them in a file
// and prints what go test prints without -json: each package's result,
// and the output of the tests that failed. Later, it lists the tests the
// kept events say were skipped, each with its reason.
//
//	go test -json ./... | go run ./scripts/testlog -save tests.json
//	go run ./scripts/testlog -skips tests.json ...
//
// scripts/check.sh runs its tests through it when RAMUS_TEST_JSON names a
// folder, and CI lists the skipped tests from there: a test that skips
// where it should run passes without testing anything, which only such a
// list shows, and the tests run once.
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
}

func main() {
	save := flag.String("save", "", "the file to keep the events read from standard input in")
	skips := flag.Bool("skips", false, "list the tests skipped in the files named, each with its reason")
	flag.Parse()
	var err error
	if *skips {
		err = listSkips(os.Stdout, flag.Args())
	} else {
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
