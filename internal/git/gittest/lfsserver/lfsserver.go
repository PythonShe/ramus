// Package lfsserver is a Git LFS server that keeps file locks and nothing
// else, for tests of what git-lfs reads and writes through the locking
// API. It runs on the loopback interface for the length of a test.
package lfsserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Server keeps locks for one user, Me; every request is taken as theirs.
type Server struct {
	// URL is the LFS endpoint, for lfs.url.
	URL string
	Me  string
	// NoVerify makes the server answer as one without lock verification.
	// Atomic: a test sets it while the server runs.
	NoVerify atomic.Bool

	mu    sync.Mutex
	locks []Lock
	next  int
}

// Lock is a lock as the API sends it.
type Lock struct {
	ID       string    `json:"id"`
	Path     string    `json:"path"`
	LockedAt time.Time `json:"locked_at"`
	Owner    struct {
		Name string `json:"name"`
	} `json:"owner"`
}

// New starts a server whose user is me; it stops when the test ends.
func New(t testing.TB, me string) *Server {
	s := &Server{Me: me}
	hs := httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(hs.Close)
	s.URL = hs.URL
	return s
}

// Add locks path as owner.
func (s *Server) Add(path, owner string) Lock {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.add(path, owner)
}

func (s *Server) add(path, owner string) Lock {
	s.next++
	l := Lock{ID: strconv.Itoa(s.next), Path: path, LockedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	l.Owner.Name = owner
	s.locks = append(s.locks, l)
	return l
}

// Locks is the locks held now.
func (s *Server) Locks() []Lock {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Lock(nil), s.locks...)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := strings.TrimSuffix(r.URL.Path, "/")
	reply := func(code int, v any) {
		w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	switch {
	case path == "/locks" && r.Method == http.MethodGet:
		var out []Lock
		for _, l := range s.locks {
			if p := r.URL.Query().Get("path"); p != "" && p != l.Path {
				continue
			}
			if id := r.URL.Query().Get("id"); id != "" && id != l.ID {
				continue
			}
			out = append(out, l)
		}
		reply(http.StatusOK, map[string]any{"locks": nonNil(out)})
	case path == "/locks" && r.Method == http.MethodPost:
		var req struct{ Path string }
		json.NewDecoder(r.Body).Decode(&req)
		for _, l := range s.locks {
			if l.Path == req.Path {
				reply(http.StatusConflict, map[string]any{"lock": l, "message": "already created lock"})
				return
			}
		}
		reply(http.StatusCreated, map[string]any{"lock": s.add(req.Path, s.Me)})
	case path == "/locks/verify" && r.Method == http.MethodPost:
		if s.NoVerify.Load() {
			reply(http.StatusNotFound, map[string]any{"message": "not found"})
			return
		}
		var ours, theirs []Lock
		for _, l := range s.locks {
			if l.Owner.Name == s.Me {
				ours = append(ours, l)
			} else {
				theirs = append(theirs, l)
			}
		}
		reply(http.StatusOK, map[string]any{"ours": nonNil(ours), "theirs": nonNil(theirs)})
	case strings.HasPrefix(path, "/locks/") && strings.HasSuffix(path, "/unlock") && r.Method == http.MethodPost:
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/locks/"), "/unlock")
		var req struct{ Force bool }
		json.NewDecoder(r.Body).Decode(&req)
		for i, l := range s.locks {
			if l.ID != id {
				continue
			}
			if l.Owner.Name != s.Me && !req.Force {
				reply(http.StatusForbidden, map[string]any{"message": "lock held by " + l.Owner.Name})
				return
			}
			s.locks = append(s.locks[:i], s.locks[i+1:]...)
			reply(http.StatusOK, map[string]any{"lock": l})
			return
		}
		reply(http.StatusNotFound, map[string]any{"message": "no such lock"})
	default:
		reply(http.StatusNotFound, map[string]any{"message": "not found"})
	}
}

func nonNil(ls []Lock) []Lock {
	if ls == nil {
		return []Lock{}
	}
	return ls
}
