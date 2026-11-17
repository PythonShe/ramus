package session

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ramus/internal/forge"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/repo"
)

// fakeAvatars is a host that knows the people of some commits and has
// some pictures, and counts what it was asked.
type fakeAvatars struct {
	mu        sync.Mutex
	byCommit  map[string][]forge.Person
	pics      map[string][]byte
	peopleErr error
	// gate, when set, holds every answer until it is closed.
	gate      chan struct{}
	queries   [][]string
	downloads []string
}

func (f *fakeAvatars) wait(ctx context.Context) {
	f.mu.Lock()
	g := f.gate
	f.mu.Unlock()
	if g != nil {
		select {
		case <-g:
		case <-ctx.Done():
		}
	}
}

func (f *fakeAvatars) people(ctx context.Context, shas []string) (map[string][]forge.Person, error) {
	f.wait(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, slices.Clone(shas))
	if f.peopleErr != nil {
		return nil, f.peopleErr
	}
	out := map[string][]forge.Person{}
	for _, sha := range shas {
		if p, ok := f.byCommit[sha]; ok {
			out[sha] = p
		}
	}
	return out, nil
}

func (f *fakeAvatars) picture(ctx context.Context, addr string) ([]byte, error) {
	f.wait(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.downloads = append(f.downloads, addr)
	if b, ok := f.pics[addr]; ok {
		return b, nil
	}
	return nil, fmt.Errorf("%w: %s", forge.ErrNotFound, addr)
}

func (f *fakeAvatars) counts() (queries, downloads int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.queries), len(f.downloads)
}

func oidOf(n int) []byte {
	b := make([]byte, 20)
	b[19] = byte(n)
	b[18] = byte(n >> 8)
	return b
}

func pic(n int) []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{byte(n)}, 64)...)
}

func avatarURL(n int) string {
	return fmt.Sprintf("https://avatars.githubusercontent.com/u/%d?s=64", n)
}

// openAvatars opens a trusted repository whose pictures come from f.
func openAvatars(t *testing.T, f *fakeAvatars) (*Session, mainThread) {
	t.Helper()
	r := gittest.Init(t)
	r.Commit("one")
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "the repository", func() bool { return s.Repo != nil })
	s.avatarSourceHook = func(*repo.Repository) avatarSource { return f }
	s.SetForgeFind(func(forge.Kind) (*forge.CLI, error) { return nil, forge.ErrNoCLI })
	return s, m
}

// The people of a commit are asked once, its pictures downloaded once
// each, an account's addresses share its picture, and an address with no
// account downloads nothing.
func TestAvatarsArrive(t *testing.T) {
	c1, c2 := oidOf(1), oidOf(2)
	f := &fakeAvatars{
		byCommit: map[string][]forge.Person{
			hex.EncodeToString(c1): {
				{Email: "ann@example.com", Login: "ann", Avatar: avatarURL(1)},
				{Email: "bob@example.com"},
			},
			hex.EncodeToString(c2): {
				{Email: "ann@work.example", Login: "ann", Avatar: avatarURL(1)},
			},
		},
		pics: map[string][]byte{avatarURL(1): pic(1)},
	}
	s, m := openAvatars(t, f)
	s.RequestAvatars([][]byte{c1, c2})
	m.until(t, "the picture", func() bool { return s.AvatarsChanged() == 1 })
	for _, e := range []string{"Ann@Example.com", "ann@work.example"} {
		if b, ok := s.Avatar(e); !ok || !bytes.Equal(b, pic(1)) {
			t.Errorf("%s: %v", e, ok)
		}
	}
	if _, ok := s.Avatar("bob@example.com"); ok {
		t.Error("a picture for an address with no account")
	}
	for range 3 {
		s.RequestAvatars([][]byte{c1, c2})
	}
	if q, d := f.counts(); q != 1 || d != 1 {
		t.Errorf("%d queries, %d downloads", q, d)
	}
	if len(f.queries[0]) != 2 {
		t.Errorf("asked %v", f.queries[0])
	}
}

// Asking again while the answer is coming asks nothing more.
func TestAvatarsDedupe(t *testing.T) {
	c1 := oidOf(1)
	gate := make(chan struct{})
	f := &fakeAvatars{
		byCommit: map[string][]forge.Person{hex.EncodeToString(c1): {{Email: "a@x", Login: "a", Avatar: avatarURL(1)}}},
		pics:     map[string][]byte{avatarURL(1): pic(1)},
		gate:     gate,
	}
	s, m := openAvatars(t, f)
	for range 5 {
		s.RequestAvatars([][]byte{c1})
	}
	if st := s.avatars; !st.querying || len(st.queued) != 0 {
		t.Fatalf("querying %v, %d queued", st.querying, len(st.queued))
	}
	close(gate)
	m.until(t, "the picture", func() bool { return s.AvatarsChanged() == 1 })
	for range 5 {
		s.RequestAvatars([][]byte{c1})
	}
	if q, d := f.counts(); q != 1 || d != 1 {
		t.Errorf("%d queries, %d downloads", q, d)
	}
}

// A commit the host lacks is not asked about again for a while, a
// picture it will not give is not asked for again, and a failure of the
// tool stops all asking for a while.
func TestAvatarsNegative(t *testing.T) {
	c1, c2 := oidOf(1), oidOf(2)
	f := &fakeAvatars{
		byCommit: map[string][]forge.Person{hex.EncodeToString(c2): {{Email: "gone@x", Login: "gone", Avatar: avatarURL(9)}}},
	}
	s, m := openAvatars(t, f)
	s.RequestAvatars([][]byte{c1, c2})
	m.until(t, "the answers", func() bool {
		_, d := f.counts()
		return d == 1 && len(s.avatars.fetching) == 0 && !s.avatars.querying
	})
	s.RequestAvatars([][]byte{c1, c2})
	if q, d := f.counts(); q != 1 || d != 1 {
		t.Fatalf("%d queries, %d downloads", q, d)
	}
	if _, ok := s.Avatar("gone@x"); ok {
		t.Error("a picture the host would not give")
	}

	// Once old, the missing commit is asked about again; it may be pushed.
	s.avatars.missing.Put(string(c1), time.Time{})
	s.RequestAvatars([][]byte{c1})
	m.until(t, "the second query", func() bool { q, _ := f.counts(); return q == 2 && !s.avatars.querying })

	f.mu.Lock()
	f.peopleErr = &forge.LoginError{Program: "gh", Host: "github.com"}
	f.mu.Unlock()
	s.avatars.missing.Clear()
	s.RequestAvatars([][]byte{c1})
	m.until(t, "the failure", func() bool { return !s.avatars.paused.IsZero() && !s.avatars.querying })
	s.RequestAvatars([][]byte{c1, oidOf(3)})
	if q, _ := f.counts(); q != 3 {
		t.Fatalf("%d queries while paused", q)
	}
	s.avatars.paused = time.Time{}
	s.RequestAvatars([][]byte{c1})
	m.until(t, "asking again", func() bool { q, _ := f.counts(); return q == 4 })
}

// The pictures stay within their budget, the oldest dropped, and one
// dropped is downloaded again when its commit is shown again.
func TestAvatarsBudget(t *testing.T) {
	const n = 40
	big := func(i int) []byte { return append(pic(i), make([]byte, forge.MaxAvatar-100)...) }
	f := &fakeAvatars{byCommit: map[string][]forge.Person{}, pics: map[string][]byte{}}
	var oids [][]byte
	for i := range n {
		oids = append(oids, oidOf(i))
		f.byCommit[hex.EncodeToString(oidOf(i))] = []forge.Person{{Email: fmt.Sprintf("p%d@x", i), Login: "p", Avatar: avatarURL(i)}}
		f.pics[avatarURL(i)] = big(i)
	}
	s, m := openAvatars(t, f)
	for _, oid := range oids {
		s.RequestAvatars([][]byte{oid})
	}
	m.until(t, "the pictures", func() bool { _, d := f.counts(); return d == n && len(s.avatars.fetching) == 0 })
	st := s.avatars
	if st.pics.Bytes() > AvatarsBudget || st.pics.Len() >= n {
		t.Fatalf("%d pictures, %d bytes", st.pics.Len(), st.pics.Bytes())
	}
	var dropped []byte
	for i, oid := range oids {
		if _, ok := s.Avatar(fmt.Sprintf("P%d@X", i)); !ok {
			dropped = oid
			break
		}
	}
	if dropped == nil {
		t.Fatal("nothing dropped")
	}
	s.RequestAvatars([][]byte{dropped})
	m.until(t, "the picture again", func() bool { _, d := f.counts(); return d == n+1 && len(s.avatars.fetching) == 0 })
	if q, _ := f.counts(); q < 1 || q > n {
		t.Errorf("%d queries", q)
	}
}

// With the settings off, nothing is asked and nothing is shown; an
// untrusted repository asks nothing.
func TestAvatarsOff(t *testing.T) {
	c1 := oidOf(1)
	f := &fakeAvatars{
		byCommit: map[string][]forge.Person{hex.EncodeToString(c1): {{Email: "a@x", Login: "a", Avatar: avatarURL(1)}}},
		pics:     map[string][]byte{avatarURL(1): pic(1)},
	}
	s, m := openAvatars(t, f)
	s.RequestAvatars([][]byte{c1})
	m.until(t, "the picture", func() bool { return s.AvatarsChanged() == 1 })
	s.SetForgeFind(nil)
	if _, ok := s.Avatar("a@x"); ok || s.AvatarsChanged() != 2 {
		t.Errorf("off: shown %v, changed %d", ok, s.AvatarsChanged())
	}
	s.RequestAvatars([][]byte{c1})
	if q, _ := f.counts(); q != 1 || s.avatars != nil {
		t.Errorf("off: %d queries", q)
	}

	r := gittest.Init(t)
	r.Commit("one")
	m2 := make(mainThread, 64)
	u := Open(testrun.Runner(t), trusted(t), r.Dir, m2.update)
	t.Cleanup(func() { u.Close(); <-u.Done() })
	m2.until(t, "the repository", func() bool { return u.Repo != nil })
	u.avatarSourceHook = func(*repo.Repository) avatarSource { return f }
	u.SetForgeFind(func(forge.Kind) (*forge.CLI, error) { return nil, forge.ErrNoCLI })
	u.RequestAvatars([][]byte{c1})
	if u.avatars != nil {
		t.Error("an untrusted repository asks")
	}
}

// The tool's own source asks the repository origin names on GitHub, and
// nothing for a remote elsewhere.
func TestForgeAvatarsWhere(t *testing.T) {
	for _, tc := range []struct{ origin, upstream, want string }{
		{"https://github.com/me/r.git", "", "me/r"},
		{"https://gitlab.com/me/r.git", "git@github.com:up/r.git", "up/r"},
		{"https://example.com/me/r.git", "", ""},
	} {
		r := gittest.Init(t)
		r.Commit("one")
		r.Git("remote", "add", "origin", tc.origin)
		if tc.upstream != "" {
			r.Git("remote", "add", ParentRemote, tc.upstream)
		}
		rp, err := repo.Open(context.Background(), testrun.Runner(t), r.Dir, func(repo.Location) bool { return true })
		if err != nil {
			t.Fatal(err)
		}
		f := &forgeAvatars{r: rp, find: func(k forge.Kind) (*forge.CLI, error) { return &forge.CLI{Kind: k}, nil }}
		_, fr, err := f.where()
		rp.Close()
		if got := fr.Path; got != tc.want || (tc.want == "") != (err != nil) {
			t.Errorf("%s, %s: %q, %v", tc.origin, tc.upstream, got, err)
		}
		if tc.want == "" && !strings.Contains(err.Error(), "GitHub") {
			t.Errorf("err %v", err)
		}
	}
}
