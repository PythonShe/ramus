package forge

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"ramus/internal/git/userenv"
)

// ErrUnknownHost is a host the tool is not logged in to. Parse takes any
// github.* or gitlab.* host for a forge, so a remote can name one of its
// own choosing; asking it would run the tool with the user's environment,
// whose GH_ENTERPRISE_TOKEN or GITLAB_TOKEN goes to whatever host it is
// told. Such a host is not asked.
var ErrUnknownHost = errors.New("forge: the host's command-line tool is not logged in to it")

// hostsFileMax bounds the config file read: it holds a few hosts.
const hostsFileMax = 1 << 20

// knownHosts are the hosts the tool's own config names, read again when
// the file changes, so a login made while Ramus runs counts.
type knownHosts struct {
	mu    sync.Mutex
	files []string
	stamp string
	hosts map[string]bool
}

// Knows reports whether host may be asked: the public host of the kind,
// the host the environment points the tool at, or one its config says it
// is logged in to. It reads the config file, so it is called off the
// main thread.
func (c *CLI) Knows(host string) bool {
	host = strings.ToLower(host)
	switch {
	case host == "":
		return false
	case c.Kind == GitHub && host == "github.com", c.Kind == GitLab && host == "gitlab.com":
		return true
	}
	for _, name := range hostVars(c.Kind) {
		if v, ok := envValue(c.env, name); ok && strings.EqualFold(v, host) {
			return true
		}
	}
	if c.known == nil {
		return false
	}
	return c.known.has(c.Kind, host)
}

func (k *knownHosts) has(kind Kind, host string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	stamp := ""
	for _, f := range k.files {
		if fi, err := os.Stat(f); err == nil {
			stamp += f + "\x00" + fi.ModTime().Format(time.RFC3339Nano) + "\x00" + strconv.FormatInt(fi.Size(), 10) + "\x00"
		}
	}
	if k.hosts == nil || stamp != k.stamp {
		k.stamp, k.hosts = stamp, map[string]bool{}
		for _, f := range k.files {
			b, err := readCapped(f)
			if err != nil {
				continue
			}
			for _, h := range configHosts(kind, b) {
				h = strings.ToLower(h)
				// Parse keeps the host name without its port.
				name, _, _ := strings.Cut(h, ":")
				k.hosts[h], k.hosts[name] = true, true
			}
		}
	}
	return k.hosts[host]
}

func readCapped(name string) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, hostsFileMax))
}

// hostVars are the variables that point the tool at a host of the user's
// choosing.
func hostVars(k Kind) []string {
	if k == GitLab {
		return []string{"GITLAB_HOST", "GL_HOST"}
	}
	return []string{"GH_HOST"}
}

// goos is the system the tools run on, which a test may play.
var goos = runtime.GOOS

// envValue is the value of the variable name in env, the last one set,
// its name in any case on Windows only (userenv.SameKey); ok is false
// for none, or one empty.
func envValue(env []string, name string) (string, bool) {
	v := userenv.Lookup(env, name)
	return v, v != ""
}

// configFiles are where the tool of kind keeps the hosts it is logged
// in to, as the tool itself looks for them.
func configFiles(env []string, k Kind) []string {
	home, _ := envValue(env, "HOME")
	if goos == "windows" {
		if p, ok := envValue(env, "USERPROFILE"); ok {
			home = p
		}
	}
	xdg, hasXDG := envValue(env, "XDG_CONFIG_HOME")
	var dirs []string
	switch k {
	case GitHub:
		switch d, ok := envValue(env, "GH_CONFIG_DIR"); {
		case ok:
			dirs = append(dirs, d)
		case hasXDG:
			dirs = append(dirs, filepath.Join(xdg, "gh"))
		case goos == "windows":
			if app, ok := envValue(env, "APPDATA"); ok {
				dirs = append(dirs, filepath.Join(app, "GitHub CLI"))
			}
		}
		if len(dirs) == 0 && home != "" {
			dirs = append(dirs, filepath.Join(home, ".config", "gh"))
		}
		return files(dirs, "hosts.yml")
	case GitLab:
		if d, ok := envValue(env, "GLAB_CONFIG_DIR"); ok {
			return files([]string{d}, "config.yml")
		}
		if hasXDG {
			dirs = append(dirs, filepath.Join(xdg, "glab-cli"))
		}
		if home != "" {
			dirs = append(dirs, filepath.Join(home, ".config", "glab-cli"))
			// Newer releases keep it in the system's config folder.
			switch goos {
			case "darwin":
				dirs = append(dirs, filepath.Join(home, "Library", "Application Support", "glab-cli"))
			case "windows":
				if app, ok := envValue(env, "APPDATA"); ok {
					dirs = append(dirs, filepath.Join(app, "glab-cli"))
				}
			}
		}
		return files(dirs, "config.yml")
	}
	return nil
}

func files(dirs []string, name string) []string {
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if filepath.IsAbs(d) {
			out = append(out, filepath.Join(d, name))
		}
	}
	return out
}

// configHosts reads the host names from the tool's config: the top-level
// keys of gh's hosts.yml, the keys under hosts: in glab's config.yml.
// Both are plain block YAML the tools write themselves, so a reader of
// keys and indentation is enough; values, tokens among them, are skipped.
func configHosts(k Kind, b []byte) []string {
	var out []string
	inHosts := k == GitHub
	childIndent := -1
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t\r")
		trimmed := strings.TrimLeft(line, " ")
		if trimmed == "" || trimmed[0] == '#' {
			continue
		}
		indent := len(line) - len(trimmed)
		// A key with a block under it ends its line with the colon; one
		// with a value inline is no host. A host's key may hold a port.
		key, block := strings.CutSuffix(trimmed, ":")
		key = strings.Trim(key, `"'`)
		if k == GitLab {
			if indent == 0 {
				inHosts, childIndent = key == "hosts" && block, -1
				continue
			}
			if !inHosts {
				continue
			}
			if childIndent < 0 {
				childIndent = indent
			}
			if indent != childIndent {
				continue
			}
		} else if indent != 0 {
			continue
		}
		if block && hostName(key) {
			out = append(out, key)
		}
	}
	return out
}

// hostName reports a DNS name, optionally with a port.
func hostName(s string) bool {
	if s == "" || len(s) > 255 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '-', c == ':':
		default:
			return false
		}
	}
	return true
}
