// Package forge knows the hosts a repository's remotes may be on, GitHub
// and GitLab: the web pages of a branch's pull requests, built from the
// remote URL, and what the user's own gh or glab says of them. Ramus
// holds no token: the command-line tool is logged in, or nothing is read.
package forge

import (
	"net/url"
	"strconv"
	"strings"
)

// Kind is the kind of host.
type Kind uint8

const (
	Unknown Kind = iota
	GitHub
	GitLab
)

// Repo is a repository on a forge.
type Repo struct {
	Kind Kind
	// Host is the web host, as github.com or a company's GitHub
	// Enterprise or GitLab host.
	Host string
	// Path is owner/name on GitHub, group/.../project on GitLab.
	Path string
}

// Parse reads the repository a remote URL names, in git's URL forms.
func Parse(remote string) (Repo, bool) {
	host, path, ok := HostPath(remote)
	if !ok {
		return Repo{}, false
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	parts := strings.Split(path, "/")
	for _, p := range parts {
		if !namePart(p) {
			return Repo{}, false
		}
	}
	r := Repo{Path: path}
	switch {
	case host == "github.com" || host == "ssh.github.com" || strings.HasPrefix(host, "github.com-"):
		// github.com-work is the usual ssh alias of a second account.
		r.Kind, r.Host = GitHub, "github.com"
	case strings.HasPrefix(host, "github."):
		r.Kind, r.Host = GitHub, host
	case host == "gitlab.com" || host == "altssh.gitlab.com" || strings.HasPrefix(host, "gitlab.com-"):
		r.Kind, r.Host = GitLab, "gitlab.com"
	case strings.HasPrefix(host, "gitlab."):
		r.Kind, r.Host = GitLab, host
	default:
		return Repo{}, false
	}
	if r.Kind == GitHub && len(parts) != 2 || len(parts) < 2 {
		return Repo{}, false
	}
	return r, true
}

// namePart reports a path part a forge allows: letters, digits, '.',
// '-' and '_', and not a path of its own.
func namePart(p string) bool {
	if p == "" || p == "." || p == ".." {
		return false
	}
	for _, c := range p {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// HostPath reads the host and path of a remote URL in git's forms:
// https://host/path, ssh://user@host:port/path and user@host:path.
func HostPath(remote string) (host, path string, ok bool) {
	if strings.Contains(remote, "://") {
		u, err := url.Parse(remote)
		if err != nil || u.Hostname() == "" {
			return "", "", false
		}
		switch u.Scheme {
		case "https", "http", "ssh", "git":
		default:
			return "", "", false
		}
		return strings.ToLower(u.Hostname()), u.Path, true
	}
	at, rest, found := strings.Cut(remote, "@")
	if !found {
		rest = at
	}
	h, p, found := strings.Cut(rest, ":")
	if !found || h == "" || strings.Contains(h, "/") {
		return "", "", false
	}
	return strings.ToLower(h), p, true
}

// Web is the repository's page.
func (r Repo) Web() string { return "https://" + r.Host + "/" + r.Path }

// Name is what the host calls a pull request.
func (r Repo) Name() string {
	if r.Kind == GitLab {
		return "Merge Request"
	}
	return "Pull Request"
}

// BranchPulls is the page of the open pull requests from branch, as the
// remote names it.
func (r Repo) BranchPulls(branch string) string {
	if r.Kind == GitLab {
		return r.Web() + "/-/merge_requests?state=opened&source_branch=" + url.QueryEscape(branch)
	}
	return r.Web() + "/pulls?q=" + url.QueryEscape("is:pr is:open head:"+branch)
}

// NewPull is the page that starts a pull request from branch.
func (r Repo) NewPull(branch string) string {
	if r.Kind == GitLab {
		return r.Web() + "/-/merge_requests/new?" + url.QueryEscape("merge_request[source_branch]") + "=" + url.QueryEscape(branch)
	}
	return r.Web() + "/compare/" + pathEscape(branch) + "?expand=1"
}

// pathEscape escapes a branch name for a URL path, keeping its slashes.
func pathEscape(branch string) string {
	parts := strings.Split(branch, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// Checks is the state of a pull request's checks, or pipeline.
type Checks uint8

const (
	NoChecks Checks = iota
	ChecksPending
	ChecksPassing
	ChecksFailing
)

func (c Checks) String() string {
	switch c {
	case ChecksPending:
		return "checks running"
	case ChecksPassing:
		return "checks passed"
	case ChecksFailing:
		return "checks failed"
	}
	return ""
}

// Pull is an open pull request, or merge request.
type Pull struct {
	Number int
	Title  string
	URL    string
	// Branch is its head branch, as the remote names it.
	Branch string
	Draft  bool
	Checks Checks
}

// Ref is how a pull request is written: #12 on GitHub, !12 on GitLab.
func (p Pull) Ref(k Kind) string {
	if k == GitLab {
		return "!" + strconv.Itoa(p.Number)
	}
	return "#" + strconv.Itoa(p.Number)
}
