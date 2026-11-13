// Package userenv recovers the environment a user's terminal has, for an
// app started from the Dock, Finder or a desktop launcher, which inherits
// only the session's bare environment: no Homebrew on PATH, no ssh agent
// socket, no GnuPG home. Without it, git, ssh and gpg work in the terminal
// and fail in the app.
package userenv

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Keys are the variables taken from the login shell; the rest of the
// app's environment stays as it was.
var Keys = []string{
	"PATH", "SSH_AUTH_SOCK", "SSH_AGENT_PID", "GNUPGHOME", "GPG_AGENT_INFO",
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy", "ALL_PROXY",
	"LANG", "LC_ALL", "LC_CTYPE",
}

// Timeout bounds the shell: a startup file that is slow or broken must not
// hang the app.
const Timeout = 10 * time.Second

const marker = "\x1fRAMUS-ENV\x1f"

// Result is the resolved environment.
type Result struct {
	// Env is os.Environ() with Keys replaced by the shell's values.
	Env []string
	// Shell is the shell asked, empty when none was.
	Shell string
	// Err says why the shell's environment could not be read; Env is then
	// the app's own, which still works for anything installed in the
	// usual places.
	Err error
}

// NeedsResolving reports whether the app was started outside a terminal.
// A terminal gives the app its environment already.
func NeedsResolving() bool {
	if runtime.GOOS == "windows" {
		return false // the environment comes from the registry either way
	}
	return os.Getenv("TERM") == "" && os.Getenv("TERM_PROGRAM") == ""
}

// Resolve asks the user's login shell for its environment, once, within
// Timeout.
func Resolve(ctx context.Context) Result {
	base := os.Environ()
	shell := os.Getenv("SHELL")
	if shell == "" || !filepath.IsAbs(shell) {
		shell = "/bin/sh"
		if runtime.GOOS == "darwin" {
			shell = "/bin/zsh"
		}
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	// -i -l loads what an interactive terminal loads; the markers fence
	// the env output off from whatever the startup files print.
	script := "printf '" + marker + "'; command env -0; printf '" + marker + "'"
	cmd := exec.CommandContext(ctx, shell, "-i", "-l", "-c", script)
	cmd.Stdin = nil
	cmd.Dir = homeDir()
	cmd.Env = append(base, "RAMUS_RESOLVING_ENV=1")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = nil
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if ctx.Err() != nil {
		err = errors.New("userenv: login shell did not finish within " + Timeout.String())
	}
	got, perr := parse(out.Bytes())
	if perr != nil {
		if err == nil {
			err = perr
		}
		return Result{Env: base, Shell: shell, Err: err}
	}
	return Result{Env: merge(base, got), Shell: shell}
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "/"
}

func parse(out []byte) (map[string]string, error) {
	start := bytes.Index(out, []byte(marker))
	if start < 0 {
		return nil, errors.New("userenv: no output from the login shell")
	}
	rest := out[start+len(marker):]
	end := bytes.Index(rest, []byte(marker))
	if end < 0 {
		return nil, errors.New("userenv: login shell output cut short")
	}
	env := map[string]string{}
	for _, kv := range bytes.Split(rest[:end], []byte{0}) {
		k, v, ok := strings.Cut(string(kv), "=")
		if ok && k != "" {
			env[k] = v
		}
	}
	if env["PATH"] == "" {
		return nil, errors.New("userenv: login shell has no PATH")
	}
	return env, nil
}

func merge(base []string, shell map[string]string) []string {
	keep := map[string]bool{}
	for _, k := range Keys {
		keep[k] = true
	}
	var out []string
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if keep[k] {
			if _, ok := shell[k]; ok {
				continue
			}
		}
		out = append(out, kv)
	}
	for _, k := range Keys {
		if v, ok := shell[k]; ok {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// Lookup returns the value of key in env.
func Lookup(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(env[i], "="); ok && k == key {
			return v
		}
	}
	return ""
}
