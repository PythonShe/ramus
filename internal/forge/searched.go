package forge

import (
	"strings"
	"sync"
)

// searched remembers the lists of a branch's pull requests read past
// their first page, as far as they are ever read, without one from the
// head asked about. GitHub's lists are ordered by update, latest first;
// GitLab's list of every state is too, which holds its open ones. So a
// pull request of the head's made, reopened or otherwise changed since
// is on a first page, which each lookup reads: the pages after are not
// read again, which for a name many forks' branches share (main,
// patch-1) would be a few calls more at every refresh.
type searched struct {
	mu sync.Mutex
	m  map[string]struct{}
}

// maxSearched bounds the lists remembered; past it, they are forgotten
// all at once and read again.
const maxSearched = 4096

// searchKey names the list of base's pull requests from branch of head,
// open or in any other state (list).
func searchKey(base Repo, head, branch, list string) string {
	return strings.Join([]string{base.Host, base.Path, head, branch, list}, "\x00")
}

func (s *searched) has(key string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.m[key]
	return ok
}

func (s *searched) add(key string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil || len(s.m) >= maxSearched {
		s.m = map[string]struct{}{}
	}
	s.m[key] = struct{}{}
}
