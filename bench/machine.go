package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Machine is what a result was measured on.
type Machine struct {
	Hardware, CPU string
	Cores         int
	Memory        int64
	OS, OSVersion string
	Git, Go       string
	GOMAXPROCS    int
}

func (m Machine) String() string {
	return fmt.Sprintf("%s, %s, %d cores, %s; %s %s; %s; %s, GOMAXPROCS %d",
		m.Hardware, m.CPU, m.Cores, mib(m.Memory), m.OS, m.OSVersion, m.Git, m.Go, m.GOMAXPROCS)
}

func machine() Machine {
	m := Machine{Cores: runtime.NumCPU(), OS: runtime.GOOS + "/" + runtime.GOARCH, Go: runtime.Version(), GOMAXPROCS: runtime.GOMAXPROCS(0)}
	m.Git = output("git", "--version")
	switch runtime.GOOS {
	case "darwin":
		m.Hardware = output("sysctl", "-n", "hw.model")
		m.CPU = output("sysctl", "-n", "machdep.cpu.brand_string")
		fmt.Sscan(output("sysctl", "-n", "hw.memsize"), &m.Memory)
		m.OSVersion = "macOS " + output("sw_vers", "-productVersion")
	case "linux":
		if b, err := os.ReadFile("/sys/devices/virtual/dmi/id/product_name"); err == nil {
			m.Hardware = strings.TrimSpace(string(b))
		}
		if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
			for _, l := range strings.Split(string(b), "\n") {
				if k, v, ok := strings.Cut(l, ":"); ok && strings.TrimSpace(k) == "model name" {
					m.CPU = strings.TrimSpace(v)
					break
				}
			}
		}
		if b, err := os.ReadFile("/proc/meminfo"); err == nil {
			var kb int64
			fmt.Sscanf(string(b), "MemTotal: %d kB", &kb)
			m.Memory = kb << 10
		}
		m.OSVersion = output("uname", "-r")
	case "windows":
		m.CPU = os.Getenv("PROCESSOR_IDENTIFIER")
		m.OSVersion = output("cmd", "/c", "ver")
	}
	return m
}

// output is a command's first line, "" when it fails.
func output(name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}
