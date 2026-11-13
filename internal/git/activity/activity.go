// Package activity keeps one repository's commits as compact records,
// the authors' identities and times only, so statistics across many
// repositories can be counted again for any range or person without
// reading a commit twice.
package activity

import (
	"context"
	"errors"
	"strings"
	"sync"

	"ramus/internal/git/catfile"
	"ramus/internal/git/mailmap"
	"ramus/internal/git/object"
	"ramus/internal/git/stats"
)

// Records are a repository's commits, one entry each, in no order. They
// are pointer-free but for the identity table, so many repositories'
// records stay out of the collector's way.
type Records struct {
	// Per commit: author time (seconds) and zone (minutes east of UTC),
	// commit time, and the author's identity, an index in the table.
	At     []int64
	Zone   []int16
	Commit []int64
	Who    []uint32
	// Identities after the mailmap: name and address back to back in
	// buf, idEnds holding where each ends (name end, address end).
	buf    []byte
	idEnds []uint32
	ids    map[string]uint32
}

// Len returns the number of commits.
func (r *Records) Len() int { return len(r.At) }

// Bytes returns the memory the records hold, for cache budgets.
func (r *Records) Bytes() int {
	return cap(r.At)*8 + cap(r.Zone)*2 + cap(r.Commit)*8 + cap(r.Who)*4 + cap(r.buf) + cap(r.idEnds)*4 + len(r.ids)*48
}

// Identities returns the number of distinct identities.
func (r *Records) Identities() int { return len(r.idEnds) / 2 }

// Identity returns identity i's name and address.
func (r *Records) Identity(i uint32) (name, email string) {
	start := uint32(0)
	if i > 0 {
		start = r.idEnds[2*i-1]
	}
	mid, end := r.idEnds[2*i], r.idEnds[2*i+1]
	return string(r.buf[start:mid]), string(r.buf[mid:end])
}

func (r *Records) identity(name, email string) uint32 {
	if r.ids == nil {
		r.ids = map[string]uint32{}
	}
	key := name + "\x00" + email
	if i, ok := r.ids[key]; ok {
		return i
	}
	i := uint32(len(r.idEnds) / 2)
	r.buf = append(r.buf, name...)
	r.idEnds = append(r.idEnds, uint32(len(r.buf)))
	r.buf = append(r.buf, email...)
	r.idEnds = append(r.idEnds, uint32(len(r.buf)))
	r.ids[key] = i
	return i
}

// add appends one commit.
func (r *Records) add(author, committer object.Signature, mm *mailmap.Map) {
	name, email := mm.Lookup(string(author.Name), string(author.Email))
	r.At = append(r.At, author.When)
	r.Zone = append(r.Zone, author.Offset)
	r.Commit = append(r.Commit, committer.When)
	r.Who = append(r.Who, r.identity(name, email))
}

// merge appends o's commits.
func (r *Records) merge(o *Records) {
	m := make([]uint32, o.Identities())
	for i := range m {
		m[i] = r.identity(o.Identity(uint32(i)))
	}
	r.At = append(r.At, o.At...)
	r.Zone = append(r.Zone, o.Zone...)
	r.Commit = append(r.Commit, o.Commit...)
	for _, w := range o.Who {
		r.Who = append(r.Who, m[w])
	}
}

// batch is the commits read in one cat-file round trip.
const batch = 2048

// Read reads the commits ids names through readers, one goroutine each,
// into records, mapping authors through mm (which may be nil). progress,
// when set, gets the number read every batch, from a reading goroutine.
func Read(ctx context.Context, readers []stats.Reader, ids [][]byte, mm *mailmap.Map, progress func(read int)) (*Records, error) {
	if len(readers) == 0 {
		return nil, errors.New("activity: no reader")
	}
	work := make(chan [][]byte)
	var (
		mu    sync.Mutex
		out   = &Records{}
		read  int
		first error
		wg    sync.WaitGroup
	)
	for _, rd := range readers {
		wg.Go(func() {
			part := &Records{}
			for ids := range work {
				err := rd.Read(ids, func(_ int, o catfile.Object) error {
					if o.Type != "commit" {
						return nil
					}
					if c, err := object.ParseCommit(o.Data); err == nil {
						part.add(c.Author, c.Committer, mm)
					}
					return nil
				})
				mu.Lock()
				if err != nil && first == nil {
					first = err
				}
				read += len(ids)
				n := read
				mu.Unlock()
				if progress != nil {
					progress(n)
				}
			}
			mu.Lock()
			out.merge(part)
			mu.Unlock()
		})
	}
	for i := 0; i < len(ids) && ctx.Err() == nil; i += batch {
		select {
		case work <- ids[i:min(i+batch, len(ids))]:
		case <-ctx.Done():
		}
	}
	close(work)
	wg.Wait()
	if first == nil {
		first = ctx.Err()
	}
	if first != nil {
		return nil, first
	}
	return out, nil
}

// Emails is a set of addresses, compared ignoring case.
type Emails map[string]bool

// NewEmails returns the set of the given addresses.
func NewEmails(list ...string) Emails {
	e := Emails{}
	for _, s := range list {
		if s = strings.TrimSpace(s); s != "" {
			e[strings.ToLower(s)] = true
		}
	}
	return e
}

// Mine returns, per identity, whether its address is in e: the user's
// own commits.
func (r *Records) Mine(e Emails) []bool {
	mine := make([]bool, r.Identities())
	for i := range mine {
		_, email := r.Identity(uint32(i))
		mine[i] = e[strings.ToLower(email)]
	}
	return mine
}
