package session

import (
	"path/filepath"
	"slices"
	"sync"
)

// pullsInUse are the pull requests each open session keeps the fetched
// refs of, by the git directory those refs are in. The app's windows each
// have sessions of their own over the same repositories, whose refs they
// share: one's clean-up (ForgetPulls) must not delete what another
// reviews.
var pullsInUse = struct {
	mu sync.Mutex
	by map[string]map[*Session][]int
}{by: map[string]map[*Session][]int{}}

// usePulls records the pull requests s keeps in commonDir: ns, in place
// of those it kept before unless add is set.
func (s *Session) usePulls(commonDir string, ns []int, add bool) {
	d := filepath.Clean(commonDir)
	pullsInUse.mu.Lock()
	defer pullsInUse.mu.Unlock()
	if s.pullsDir != "" && s.pullsDir != d {
		s.dropPullsLocked()
	}
	m := pullsInUse.by[d]
	if m == nil {
		m = map[*Session][]int{}
		pullsInUse.by[d] = m
	}
	if add {
		m[s] = append(slices.Clone(m[s]), ns...)
	} else {
		m[s] = slices.Clone(ns)
	}
	s.pullsDir = d
}

// othersPulls are the pull requests the sessions but s keep in commonDir.
func (s *Session) othersPulls(commonDir string) []int {
	pullsInUse.mu.Lock()
	defer pullsInUse.mu.Unlock()
	var out []int
	for o, ns := range pullsInUse.by[filepath.Clean(commonDir)] {
		if o != s {
			out = append(out, ns...)
		}
	}
	return out
}

// dropPulls forgets what s keeps, as it closes.
func (s *Session) dropPulls() {
	pullsInUse.mu.Lock()
	defer pullsInUse.mu.Unlock()
	s.dropPullsLocked()
}

func (s *Session) dropPullsLocked() {
	if s.pullsDir == "" {
		return
	}
	if m := pullsInUse.by[s.pullsDir]; m != nil {
		delete(m, s)
		if len(m) == 0 {
			delete(pullsInUse.by, s.pullsDir)
		}
	}
	s.pullsDir = ""
}
