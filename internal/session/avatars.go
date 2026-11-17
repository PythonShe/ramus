package session

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"ramus/internal/forge"
	"ramus/internal/git/cache"
	"ramus/internal/git/repo"
)

// The pictures of the people who made commits, as GitHub has them, read
// through the user's gh when the settings turn pull requests on
// (SetForgeFind) and the repository's remote is on GitHub. Without them
// the views draw initials.

const (
	// AvatarsBudget bounds the pictures a repository keeps.
	AvatarsBudget = 8 << 20
	// avatarPeopleBudget bounds what is known of commits and addresses:
	// who they name, and which address has an account and which none.
	avatarPeopleBudget = 1 << 20
	// avatarFailedBudget bounds the pictures that could not be had.
	avatarFailedBudget = 64 << 10
	// avatarQueue bounds the commits, and the pictures, waiting to be
	// asked for: the newest asked go first, and past it the oldest are
	// forgotten, to be asked again when shown again.
	avatarQueue = 500
	// avatarDownloads is how many pictures one repository downloads at
	// once; the tool's own slots bound them across repositories.
	avatarDownloads = 2
	// avatarRetry is how long nothing more is asked after a failure that
	// is not one commit's or one picture's: no gh, no login, no answer.
	avatarRetry = 5 * time.Minute
	// avatarMissingAge is how long a commit the host does not have is
	// not asked about again: it may be pushed since.
	avatarMissingAge = 5 * time.Minute
)

// avatarSource reads who commits name and their pictures; forgeAvatars
// is the host's, through its tool, and tests give their own.
type avatarSource interface {
	// people is CommitPeople for full commit ids.
	people(ctx context.Context, shas []string) (map[string][]forge.Person, error)
	// picture is Avatar.
	picture(ctx context.Context, addr string) ([]byte, error)
}

// avatarStore is what one repository knows of its people's pictures, and
// what it is asking for. It is dropped with the repository, or when the
// settings turn asking off. Main thread only: the work it starts hands
// its answers back through update.
type avatarStore struct {
	r *repo.Repository
	// commits are the commits answered, each with the addresses it
	// names, lower case.
	commits *cache.Cache[string, []string]
	// missing are the commits the host did not have, and when.
	missing *cache.Cache[string, time.Time]
	// people is each address's picture address, "" for an address with
	// no account.
	people *cache.Cache[string, string]
	// pics are the pictures, by their address: several addresses of
	// one account share one.
	pics *cache.Cache[string, []byte]
	// failed are the pictures the host would not give.
	failed *cache.Cache[string, struct{}]
	// queued are the commits waiting, oldest first; asking those of the
	// query running.
	queued   []string
	inQueue  map[string]bool
	asking   map[string]bool
	querying bool
	// wanted are the pictures waiting, oldest first; fetching those
	// being downloaded.
	wanted   []string
	inWanted map[string]bool
	fetching map[string]bool
	// paused is when asking may start again after a failure.
	paused time.Time
}

func newAvatarStore(r *repo.Repository) *avatarStore {
	return &avatarStore{
		r: r,
		commits: cache.New[string, []string](avatarPeopleBudget, func(emails []string) int {
			n := 64
			for _, e := range emails {
				n += 16 + len(e)
			}
			return n
		}),
		missing:  cache.New[string, time.Time](avatarPeopleBudget, func(time.Time) int { return 96 }),
		people:   cache.New[string, string](avatarPeopleBudget, func(addr string) int { return 128 + len(addr) }),
		pics:     cache.New[string, []byte](AvatarsBudget, func(b []byte) int { return 64 + len(b) }),
		failed:   cache.New[string, struct{}](avatarFailedBudget, func(struct{}) int { return 160 }),
		inQueue:  map[string]bool{},
		asking:   map[string]bool{},
		inWanted: map[string]bool{},
		fetching: map[string]bool{},
	}
}

// SetForgeFind sets how the session finds the host's tool for what it
// asks of the host by itself, the pictures of people: the UI gives the
// one it asks about pull requests with while the settings turn them on,
// and nil when they are off, which drops the pictures and asks nothing
// more. Main thread only.
func (s *Session) SetForgeFind(find func(forge.Kind) (*forge.CLI, error)) {
	s.forgeFind = find
	if find == nil && s.avatars != nil {
		s.avatars = nil
		s.avatarsChanged++
	}
}

// AvatarsChanged counts the pictures that arrived, and the times they
// were dropped, so a view can tell when to draw again and to drop the
// pictures it decoded. Main thread only.
func (s *Session) AvatarsChanged() uint64 { return s.avatarsChanged }

// Avatar returns the picture of the person with this address, in any
// case, PNG or JPEG bytes, once it has been downloaded; it does no I/O.
// The address is the one the commit gives, not the mailmap's: the host
// knows people by it. Main thread only.
func (s *Session) Avatar(email string) ([]byte, bool) {
	st := s.avatars
	if st == nil || st.r != s.Repo || s.forgeFind == nil {
		return nil, false
	}
	addr, ok := st.people.Peek(strings.ToLower(email))
	if !ok || addr == "" {
		return nil, false
	}
	return st.pics.Get(addr)
}

// RequestAvatars asks for the pictures of the people of these commits
// (author, co-authors, committer), when the host can tell them: of a
// trusted repository whose remote is on GitHub, while SetForgeFind has a
// tool. It is cheap for commits already asked about, so a view may ask
// for the rows it shows each frame; when pictures arrive, the window is
// drawn again and AvatarsChanged counts them. Main thread only.
func (s *Session) RequestAvatars(oids [][]byte) {
	st := s.avatarStore()
	if st == nil {
		return
	}
	now := s.timeNow()
	if now.Before(st.paused) {
		// Nothing is kept waiting: the rows shown then are asked for.
		return
	}
	for _, oid := range oids {
		k := string(oid)
		if at, ok := st.missing.Peek(k); ok {
			if now.Sub(at) < avatarMissingAge {
				continue
			}
			st.missing.Remove(k)
		}
		if emails, ok := st.commits.Get(k); ok {
			if !st.wantPeople(emails) {
				// An address it names was forgotten: ask again.
				st.commits.Remove(k)
				st.queue(k)
			}
			continue
		}
		if !st.asking[k] {
			st.queue(k)
		}
	}
	s.pumpAvatars(st)
}

// avatarStore is the open repository's store, made the first time, nil
// while nothing is to be asked.
func (s *Session) avatarStore() *avatarStore {
	r := s.Repo
	if r == nil || r.Restricted || s.forgeFind == nil {
		return nil
	}
	if s.avatars == nil || s.avatars.r != r {
		if s.avatars != nil {
			s.avatarsChanged++
		}
		s.avatars = newAvatarStore(r)
	}
	return s.avatars
}

// queue puts commit k last among those waiting, the next to be asked.
func (st *avatarStore) queue(k string) {
	if st.inQueue[k] {
		for i, q := range st.queued {
			if q == k {
				st.queued = append(st.queued[:i], st.queued[i+1:]...)
				break
			}
		}
	}
	st.queued, st.inQueue[k] = append(st.queued, k), true
	if over := len(st.queued) - avatarQueue; over > 0 {
		for _, q := range st.queued[:over] {
			delete(st.inQueue, q)
		}
		st.queued = append(st.queued[:0], st.queued[over:]...)
	}
}

// wantPeople asks for the pictures of addresses emails not yet had; it
// is false when one of them is no longer known.
func (st *avatarStore) wantPeople(emails []string) bool {
	known := true
	for _, e := range emails {
		addr, ok := st.people.Get(e)
		if !ok {
			known = false
			continue
		}
		st.want(addr)
	}
	return known
}

// want puts picture addr last among those waiting, unless it is had,
// being downloaded or not to be had.
func (st *avatarStore) want(addr string) {
	if addr == "" || st.fetching[addr] {
		return
	}
	if _, ok := st.pics.Get(addr); ok {
		return
	}
	if _, ok := st.failed.Peek(addr); ok {
		return
	}
	if st.inWanted[addr] {
		for i, w := range st.wanted {
			if w == addr {
				st.wanted = append(st.wanted[:i], st.wanted[i+1:]...)
				break
			}
		}
	}
	st.wanted, st.inWanted[addr] = append(st.wanted, addr), true
	if over := len(st.wanted) - avatarQueue; over > 0 {
		for _, w := range st.wanted[:over] {
			delete(st.inWanted, w)
		}
		st.wanted = append(st.wanted[:0], st.wanted[over:]...)
	}
}

// avatarSourceFor is what the store of r reads through, made where the work runs: the tool
// is found off the main thread.
func (s *Session) avatarSourceFor(r *repo.Repository) avatarSource {
	if s.avatarSourceHook != nil {
		return s.avatarSourceHook(r)
	}
	return &forgeAvatars{r: r, find: s.forgeFind}
}

// pumpAvatars starts what st has waiting, within its bounds: one query of
// commits, avatarDownloads pictures.
func (s *Session) pumpAvatars(st *avatarStore) {
	if s.timeNow().Before(st.paused) {
		return
	}
	r := st.r
	ctx := r.Life().Context()
	update := s.update
	if !st.querying && len(st.queued) > 0 {
		n := min(len(st.queued), forge.CommitBatch)
		batch := append([]string(nil), st.queued[len(st.queued)-n:]...)
		st.queued = st.queued[:len(st.queued)-n]
		for _, k := range batch {
			delete(st.inQueue, k)
			st.asking[k] = true
		}
		st.querying = true
		src := s.avatarSourceFor(r)
		r.Life().Go(func(context.Context) {
			shas := make([]string, len(batch))
			for i, k := range batch {
				shas[i] = hex.EncodeToString([]byte(k))
			}
			got, err := src.people(ctx, shas)
			if ctx.Err() != nil {
				return
			}
			update(func() { s.peopleArrived(st, batch, shas, got, err) })
		})
	}
	for len(st.fetching) < avatarDownloads && len(st.wanted) > 0 {
		addr := st.wanted[len(st.wanted)-1]
		st.wanted = st.wanted[:len(st.wanted)-1]
		delete(st.inWanted, addr)
		st.fetching[addr] = true
		src := s.avatarSourceFor(r)
		r.Life().Go(func(context.Context) {
			b, err := src.picture(ctx, addr)
			if ctx.Err() != nil {
				return
			}
			update(func() { s.pictureArrived(st, addr, b, err) })
		})
	}
}

// peopleArrived keeps what the host said of the commits of batch.
func (s *Session) peopleArrived(st *avatarStore, batch, shas []string, got map[string][]forge.Person, err error) {
	st.querying = false
	for _, k := range batch {
		delete(st.asking, k)
	}
	if s.avatars != st {
		return
	}
	if err != nil {
		// Asked again once the pause is over and they are shown again.
		st.paused = s.timeNow().Add(avatarRetry)
		return
	}
	now := s.timeNow()
	for i, k := range batch {
		people, ok := got[shas[i]]
		if !ok {
			st.missing.Put(k, now)
			continue
		}
		emails := make([]string, 0, len(people))
		for _, p := range people {
			emails = append(emails, p.Email)
			// An address seen with an account keeps it; GitHub ties an
			// address to one account.
			if old, ok := st.people.Peek(p.Email); !ok || old == "" {
				st.people.Put(p.Email, p.Avatar)
			}
		}
		st.commits.Put(k, emails)
		st.wantPeople(emails)
	}
	s.pumpAvatars(st)
}

// pictureArrived keeps a picture downloaded, or that it is not to be had.
func (s *Session) pictureArrived(st *avatarStore, addr string, b []byte, err error) {
	delete(st.fetching, addr)
	if s.avatars != st {
		return
	}
	var rej *forge.RejectedError
	switch {
	case err == nil:
		if st.pics.Put(addr, b) {
			s.avatarsChanged++
		}
	case errors.Is(err, forge.ErrNotFound), errors.Is(err, forge.ErrTooLarge), errors.Is(err, forge.ErrNotAPicture), errors.As(err, &rej):
		st.failed.Put(addr, struct{}{})
	default:
		st.paused = s.timeNow().Add(avatarRetry)
		st.want(addr)
		return
	}
	s.pumpAvatars(st)
}

// forgeAvatars reads through the host's tool, from the repository the
// remotes name on GitHub: origin, or else the one named upstream.
// GitHub keeps a fork's commits with its parent's, so either answers
// for both.
type forgeAvatars struct {
	r    *repo.Repository
	find func(forge.Kind) (*forge.CLI, error)
}

// errNoGitHub is a repository whose remotes name no GitHub repository.
var errNoGitHub = errors.New("session: no remote is on GitHub")

func (f *forgeAvatars) where() (*forge.CLI, forge.Repo, error) {
	up, origin := RemoteURLs(f.r)
	fr, ok := forge.Parse(origin)
	if !ok || fr.Kind != forge.GitHub {
		if fr, ok = forge.Parse(up); !ok || fr.Kind != forge.GitHub {
			return nil, forge.Repo{}, errNoGitHub
		}
	}
	c, err := f.find(forge.GitHub)
	if err != nil {
		return nil, forge.Repo{}, err
	}
	return c, fr, nil
}

func (f *forgeAvatars) people(ctx context.Context, shas []string) (map[string][]forge.Person, error) {
	c, fr, err := f.where()
	if err != nil {
		return nil, err
	}
	return c.CommitPeople(ctx, fr, shas)
}

func (f *forgeAvatars) picture(ctx context.Context, addr string) ([]byte, error) {
	c, fr, err := f.where()
	if err != nil {
		return nil, err
	}
	return c.Avatar(ctx, fr, addr)
}
