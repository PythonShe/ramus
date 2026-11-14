// Command coldstart measures the app's cold start: it runs the binary
// given in its probe mode (RAMUS_PROBE_STARTUP) several times and times
// each run from exec to the line the app prints at its first frame. It
// fails when the median is over the budget.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

func main() {
	runs := flag.Int("runs", 10, "runs to time")
	budget := flag.Duration("budget", 200*time.Millisecond, "the most the median may take")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: coldstart [-runs n] [-budget d] <binary>")
		os.Exit(2)
	}
	var times []time.Duration
	for i := range *runs {
		d, inside, err := once(flag.Arg(0))
		if err != nil {
			fmt.Fprintf(os.Stderr, "run %d: %v\n", i+1, err)
			os.Exit(1)
		}
		fmt.Printf("run %d: %v from exec (%s)\n", i+1, d.Round(100*time.Microsecond), inside)
		times = append(times, d)
	}
	first := times[0]
	slices.Sort(times)
	median := times[len(times)/2]
	fmt.Printf("first run %v, median %v, min %v, max %v, budget %v\n", first.Round(100*time.Microsecond),
		median.Round(100*time.Microsecond), times[0].Round(100*time.Microsecond), times[len(times)-1].Round(100*time.Microsecond), *budget)
	if median > *budget {
		fmt.Fprintln(os.Stderr, "the median cold start is over budget")
		os.Exit(1)
	}
}

// once runs the binary in probe mode and returns the time to its
// first-frame line, and the line, which has the time the app measured
// from the start of main.
func once(bin string) (time.Duration, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin)
	cmd.Env = append(os.Environ(), "RAMUS_PROBE_STARTUP=1")
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return 0, "", err
	}
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return 0, "", err
	}
	var d time.Duration
	line := ""
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		if t := sc.Text(); strings.HasPrefix(t, "first frame ") {
			d, line = time.Since(start), t
		}
	}
	if err := cmd.Wait(); err != nil {
		return 0, "", err
	}
	if line == "" {
		return 0, "", fmt.Errorf("no first-frame line")
	}
	return d, line, nil
}
