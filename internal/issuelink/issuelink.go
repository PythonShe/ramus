// Package issuelink turns issue references in commit messages ("#123",
// "PROJ-42") into links, by pattern rules per repository. It matches text
// only: it never asks a tracker anything.
package issuelink

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Rule links text matching Pattern, a Go regular expression, to URL, in
// which $1 or ${1} stand for the pattern's groups.
type Rule struct {
	Pattern string `json:"pattern"`
	URL     string `json:"url"`
}

// Check reports whether r can be used: the pattern compiles and does not
// match empty text, and the URL is http or https.
func Check(r Rule) error {
	_, err := compile(r)
	return err
}

func compile(r Rule) (*regexp.Regexp, error) {
	if strings.TrimSpace(r.Pattern) == "" {
		return nil, errors.New("issuelink: the pattern is empty")
	}
	re, err := regexp.Compile(r.Pattern)
	if err != nil {
		return nil, fmt.Errorf("issuelink: %w", err)
	}
	if re.MatchString("") {
		return nil, errors.New("issuelink: the pattern matches empty text")
	}
	if !webURL(r.URL) {
		return nil, errors.New("issuelink: the link must start with https:// or http://")
	}
	return re, nil
}

func webURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

// Set is rules compiled; rules that fail Check are left out.
type Set struct {
	res  []*regexp.Regexp
	urls []string
}

// Compile compiles rules, earlier ones first when two match at the same
// place.
func Compile(rules []Rule) *Set {
	s := &Set{}
	for _, r := range rules {
		if re, err := compile(r); err == nil {
			s.res, s.urls = append(s.res, re), append(s.urls, r.URL)
		}
	}
	return s
}

// Len returns the number of rules in use.
func (s *Set) Len() int { return len(s.res) }

// Match is a reference found in a text, text[Start:End], and where it
// links.
type Match struct {
	Start, End int
	URL        string
}

// maxMatches bounds the links made in one text.
const maxMatches = 256

// Find returns the references in text, in order and not overlapping.
func (s *Set) Find(text string) []Match {
	if s == nil || len(s.res) == 0 {
		return nil
	}
	var all []Match
	for k, re := range s.res {
		for _, m := range re.FindAllStringSubmatchIndex(text, maxMatches) {
			if u, ok := expand(re, s.urls[k], text, m); ok {
				all = append(all, Match{m[0], m[1], u})
			}
		}
	}
	// Earliest first; at one place the earlier rule, which was added
	// first; then drop what overlaps a reference kept.
	sortMatches(all)
	var out []Match
	end := 0
	for _, m := range all {
		if m.Start >= end && len(out) < maxMatches {
			out = append(out, m)
			end = m.End
		}
	}
	return out
}

func sortMatches(ms []Match) {
	// Insertion sort keeps rule order at equal starts; lists are short.
	for i := 1; i < len(ms); i++ {
		for j := i; j > 0 && ms[j].Start < ms[j-1].Start; j-- {
			ms[j], ms[j-1] = ms[j-1], ms[j]
		}
	}
}

// expand fills the URL template from a match, escaping what the groups
// captured so the text cannot change where the link goes.
func expand(re *regexp.Regexp, tmpl, text string, m []int) (string, bool) {
	escaped := make([]int, len(m))
	var buf strings.Builder
	// Build an escaped copy of the groups and expand the template against
	// it, so each group is escaped once.
	for i := 0; i < len(m); i += 2 {
		if m[i] < 0 {
			escaped[i], escaped[i+1] = -1, -1
			continue
		}
		escaped[i] = buf.Len()
		buf.WriteString(url.PathEscape(text[m[i]:m[i+1]]))
		escaped[i+1] = buf.Len()
	}
	u := string(re.ExpandString(nil, tmpl, buf.String(), escaped))
	return u, webURL(u)
}

// FromRemote returns the rules a well-known host's issue references
// follow, from a remote URL: GitHub, GitLab and Codeberg. Others get none.
func FromRemote(remote string) []Rule {
	host, path, ok := hostPath(remote)
	if !ok {
		return nil
	}
	parts := strings.Split(strings.TrimSuffix(strings.Trim(path, "/"), ".git"), "/")
	if len(parts) < 2 {
		return nil
	}
	for _, p := range parts {
		if !namePart.MatchString(p) {
			return nil
		}
	}
	base := "https://" + host + "/" + strings.Join(parts, "/")
	switch host {
	case "github.com":
		if len(parts) != 2 {
			return nil
		}
		return []Rule{
			{`\b([\w.-]+)/([\w.-]+)#(\d+)\b`, "https://github.com/${1}/${2}/issues/${3}"},
			{`\B#(\d+)\b`, base + "/issues/${1}"},
			{`\bGH-(\d+)\b`, base + "/issues/${1}"},
		}
	case "gitlab.com":
		return []Rule{
			{`\B#(\d+)\b`, base + "/-/issues/${1}"},
			{`\B!(\d+)\b`, base + "/-/merge_requests/${1}"},
		}
	case "codeberg.org":
		if len(parts) != 2 {
			return nil
		}
		return []Rule{{`\B#(\d+)\b`, base + "/issues/${1}"}}
	}
	return nil
}

var namePart = regexp.MustCompile(`^[\w.-]+$`)

// hostPath reads the host and path of a remote URL in git's forms:
// https://host/path, ssh://user@host:port/path and user@host:path.
func hostPath(remote string) (host, path string, ok bool) {
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
