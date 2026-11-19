package forge

import (
	"strings"
	"sync"
	"time"
)

// searched remembers the lists of a branch's pull requests read past
// their first page, as far as they are ever read, without one from the
// head asked about. GitHub's lists are ordered by update, latest first;
// GitLab's list of every state is too, which holds its open ones. So a
// pull request of the head's made, reopened or otherwise changed since
// is on a first page, which each lookup reads: the pages after are not
// read again, which for a name many forks' branches share (main,
// patch-1) would be a few calls more at every refresh. Other forks'
// pull requests updated after it push one of the head's off the first
// page again, so a list is remembered for searchedFor only, and a list
// where the head's is found is forgotten: once it slides deep it is
// paged for at the next lookup.
type searched struct {
	mu sync.Mutex
	m  map[string]time.Time
}

// maxSearched bounds the lists remembered; past it, they are forgotten
// all at once and read again.
const maxSearched = 4096

// searchedFor is how long a list read to its end without the head's pull
// request is not read past its first page again: several refreshes.
const searchedFor = 30 * time.Minute

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
	at, ok := s.m[key]
	if ok && time.Since(at) >= searchedFor {
		delete(s.m, key)
		return false
	}
	return ok
}

func (s *searched) add(key string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil || len(s.m) >= maxSearched {
		s.m = map[string]time.Time{}
	}
	s.m[key] = time.Now()
}

// forget is the head's pull request found in the list of key.
func (s *searched) forget(key string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, key)
}
