// Command bench runs Ramus's benchmarks (Part 3 of the plan) and records
// each with the machine it ran on: hardware, OS, git version, the number
// of runs, p50 and p95 of every time measured, and peak memory, the app's
// and git's apart. Each run is a process of its own, so its memory is its
// own and nothing it loaded is warm for the next; the operating system's
// file cache is, as it is for a user opening a repository again.
//
//	RAMUS_FIXTURES=~/Projects/ramus-fixtures go run ./bench -runs 5 -out bench.json
//
// Cases that need a fixture the folder does not have are skipped. Nothing
// is written into a fixture: cases that write make their own repositories
// in a temporary folder, kept between runs and removed at the end, or
// kept in RAMUS_BENCH_CACHE when it is set.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

func main() {
	runs := flag.Int("runs", 5, "runs of each case")
	only := flag.String("only", "", "run the cases whose name matches this regular expression")
	out := flag.String("out", "", "write the results here as JSON, as well as the table")
	child := flag.String("child", "", "run one case once and print its result (used by bench itself)")
	list := flag.Bool("list", false, "list the cases")
	flag.Parse()
	if *child != "" {
		os.Exit(runChild(*child))
	}
	if *list {
		for _, c := range cases {
			fmt.Printf("%-28s %s\n", c.name, c.what)
		}
		return
	}
	var re *regexp.Regexp
	if *only != "" {
		var err error
		if re, err = regexp.Compile(*only); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	rep := Report{Machine: machine(), Started: time.Now().UTC(), Runs: *runs, Fixtures: os.Getenv("RAMUS_FIXTURES")}
	fmt.Println(rep.Machine)
	failed := false
	for _, c := range cases {
		if re != nil && !re.MatchString(c.name) {
			continue
		}
		res := runCase(c, *runs)
		res.print()
		rep.Cases = append(rep.Cases, res)
		failed = failed || res.Err != ""
	}
	if os.Getenv("RAMUS_BENCH_CACHE") == "" {
		os.RemoveAll(cacheDir())
	}
	if *out != "" {
		b, _ := json.MarshalIndent(rep, "", "  ")
		if err := os.WriteFile(*out, append(b, '\n'), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	if failed {
		os.Exit(1)
	}
}

// Report is one run of the benchmarks.
type Report struct {
	Machine  Machine
	Started  time.Time
	Runs     int
	Fixtures string
	Cases    []CaseResult
}

// CaseResult is a case over its runs.
type CaseResult struct {
	Name, What string
	Runs       int
	Skipped    string `json:",omitempty"`
	Err        string `json:",omitempty"`
	// Times are each measured time over the runs, by name, in order.
	Times []Dist
	// Facts are what the runs found, as the rows read, from the last run.
	Facts map[string]string `json:",omitempty"`
	// PeakApp is the most memory one run's process held, PeakGit the
	// most one git it ran held; bytes, 0 when unknown (git's on Linux,
	// both on Windows).
	PeakApp, PeakGit int64
}

// Dist is one measured time over the runs.
type Dist struct {
	Name               string
	P50, P95, Min, Max time.Duration
	All                []time.Duration
}

// childResult is what one run prints.
type childResult struct {
	Times   []named           `json:"times"`
	Facts   map[string]string `json:"facts,omitempty"`
	Skipped string            `json:"skipped,omitempty"`
	Err     string            `json:"err,omitempty"`
	PeakApp int64             `json:"peakApp"`
	PeakGit int64             `json:"peakGit"`
}

type named struct {
	Name string        `json:"name"`
	D    time.Duration `json:"d"`
}

// childTimeout bounds one run, and a soak's length more.
func childTimeout() time.Duration {
	d, _ := time.ParseDuration(os.Getenv("RAMUS_SOAK"))
	return 15*time.Minute + max(d, 0)
}

func runCase(c benchCase, runs int) CaseResult {
	res := CaseResult{Name: c.name, What: c.what}
	byName := map[string][]time.Duration{}
	var order []string
	self, err := os.Executable()
	if err != nil {
		res.Err = err.Error()
		return res
	}
	for range runs {
		ctx, cancel := context.WithTimeout(context.Background(), childTimeout())
		cmd := exec.CommandContext(ctx, self, "-child", c.name)
		var stdout bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, os.Stderr
		err := cmd.Run()
		cancel()
		var cr childResult
		if jerr := json.Unmarshal(lastLine(stdout.Bytes()), &cr); jerr != nil {
			if err == nil {
				err = jerr
			}
			res.Err = fmt.Sprintf("run %d: %v", res.Runs+1, err)
			return res
		}
		if cr.Skipped != "" {
			res.Skipped = cr.Skipped
			return res
		}
		if cr.Err != "" {
			res.Err = cr.Err
			return res
		}
		res.Runs++
		for _, t := range cr.Times {
			if _, ok := byName[t.Name]; !ok {
				order = append(order, t.Name)
			}
			byName[t.Name] = append(byName[t.Name], t.D)
		}
		res.Facts = cr.Facts
		res.PeakApp, res.PeakGit = max(res.PeakApp, cr.PeakApp), max(res.PeakGit, cr.PeakGit)
	}
	for _, n := range order {
		res.Times = append(res.Times, dist(n, byName[n]))
	}
	return res
}

func lastLine(b []byte) []byte {
	b = bytes.TrimRight(b, "\n")
	if i := bytes.LastIndexByte(b, '\n'); i >= 0 {
		return b[i+1:]
	}
	return b
}

func dist(name string, ds []time.Duration) Dist {
	s := slices.Clone(ds)
	slices.Sort(s)
	at := func(q float64) time.Duration { return s[min(len(s)-1, int(q*float64(len(s))))] }
	return Dist{Name: name, P50: at(0.5), P95: at(0.95), Min: s[0], Max: s[len(s)-1], All: ds}
}

func (r CaseResult) print() {
	switch {
	case r.Skipped != "":
		fmt.Printf("%s: skipped, %s\n", r.Name, r.Skipped)
		return
	case r.Err != "":
		fmt.Printf("%s: failed, %s\n", r.Name, r.Err)
		return
	}
	fmt.Printf("%s (%d runs), peak memory %s app, %s git\n", r.Name, r.Runs, peak(r.PeakApp), peak(r.PeakGit))
	for _, d := range r.Times {
		fmt.Printf("  %-34s p50 %-10v p95 %-10v min %-10v max %v\n", d.Name, round(d.P50), round(d.P95), round(d.Min), round(d.Max))
	}
	keys := make([]string, 0, len(r.Facts))
	for k := range r.Facts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var facts []string
	for _, k := range keys {
		facts = append(facts, k+"="+r.Facts[k])
	}
	if len(facts) > 0 {
		fmt.Printf("  %s\n", strings.Join(facts, " "))
	}
}

func round(d time.Duration) time.Duration {
	switch {
	case d >= time.Second:
		return d.Round(10 * time.Millisecond)
	case d >= time.Millisecond:
		return d.Round(10 * time.Microsecond)
	}
	return d.Round(time.Microsecond)
}

func mib(b int64) string { return fmt.Sprintf("%.1f MiB", float64(b)/(1<<20)) }

// peak is a peak memory, which 0 says was not read.
func peak(b int64) string {
	if b == 0 {
		return "unknown"
	}
	return mib(b)
}

// runChild runs case name once and prints its result as the last line.
func runChild(name string) int {
	i := slices.IndexFunc(cases, func(c benchCase) bool { return c.name == name })
	if i < 0 {
		fmt.Fprintln(os.Stderr, "no case", name)
		return 2
	}
	m := &meter{facts: map[string]string{}}
	err := cases[i].run(m)
	cr := childResult{Times: m.times, Facts: m.facts}
	switch {
	case err == errSkip:
		cr.Skipped = m.skip
	case err != nil:
		cr.Err = err.Error()
	}
	cr.PeakApp, cr.PeakGit = peakMemory()
	b, _ := json.Marshal(cr)
	fmt.Println(string(b))
	return 0
}
