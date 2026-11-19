package session

import (
	"cmp"
	"context"
	"encoding/hex"
	"slices"
	"strings"

	"ramus/internal/git/catfile"
	"ramus/internal/git/history"
	"ramus/internal/git/mailmap"
	"ramus/internal/git/object"
	"ramus/internal/git/repo"
	"ramus/internal/git/run"
	"ramus/internal/git/textenc"
)

// Person is someone who wrote commits, named as the mailmap names them.
type Person struct {
	Name, Email string
}

const (
	// AuthorCommits bounds the commits LoadAuthors reads, the newest
	// reachable from HEAD: the people who work on a repository now are
	// among them, and a long history is not read.
	AuthorCommits = 5000
	// maxPeople bounds the people kept, those with the most commits.
	maxPeople = 500
	// authorBatch is the commits read in one round trip.
	authorBatch = 512
)

// authorsLoad is the people LoadAuthors read, for the HEAD it read them
// from. Main thread only.
type authorsLoad struct {
	head    string
	people  []Person
	done    bool
	publish func([]Person)
}

// LoadAuthors hands publish, on the main thread, the people who wrote
// the newest commits reachable from HEAD, those with the most first; the
// user, as git names them in a commit, is left out. They are read once for a
// HEAD; a call while they are read replaces the publish of the one
// before. A repository without commits, or one that cannot be read, has
// nobody.
func (s *Session) LoadAuthors(publish func([]Person)) {
	r := s.Repo
	head := s.State.Head.OID
	a := &s.authors
	if r == nil || len(head) == 0 {
		publish(nil)
		return
	}
	key := string(head)
	if a.head == key {
		if a.done {
			publish(a.people)
		} else {
			a.publish = publish
		}
		return
	}
	*a = authorsLoad{head: key, publish: publish}
	tip := slices.Clone(head)
	update := s.update
	r.Life().Go(func(ctx context.Context) {
		mm := s.mailmap(ctx, r)
		people, err := readAuthors(ctx, r, mm, tip, AuthorCommits)
		me := ownEmails(ctx, r, mm)
		people = slices.DeleteFunc(people, func(p Person) bool { return slices.Contains(me, strings.ToLower(p.Email)) })
		update(func() {
			if a.head != key {
				return
			}
			a.people, a.done = people, true
			if err != nil {
				// Not kept for this HEAD: the next call reads again.
				a.head = ""
			}
			if a.publish != nil {
				a.publish(people)
				a.publish = nil
			}
		})
	})
}

// ownEmails are the addresses, lower case, git writes into a commit
// made here as its author and committer: user.email, or author.email,
// committer.email or the environment, which take its place; each also
// as the mailmap names it, as people are.
func ownEmails(ctx context.Context, r *repo.Repository, mm *mailmap.Map) []string {
	var out []string
	add := func(name, email string) {
		if email == "" {
			return
		}
		out = append(out, strings.ToLower(email))
		if _, e := mm.Lookup(name, email); e != "" && !strings.EqualFold(e, email) {
			out = append(out, strings.ToLower(e))
		}
	}
	for _, v := range []string{"GIT_AUTHOR_IDENT", "GIT_COMMITTER_IDENT"} {
		res, err := r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, WorkTree: r.WorkTree, Read: true, Untranslated: true, Args: []string{"var", v}})
		if err != nil {
			continue
		}
		// "Name <email> 1700000000 +0000"
		id := string(res.Stdout)
		lt, gt := strings.IndexByte(id, '<'), strings.IndexByte(id, '>')
		if lt >= 0 && gt > lt {
			add(strings.TrimSpace(id[:lt]), id[lt+1:gt])
		}
	}
	if len(out) == 0 {
		// git would not say, as when user.useConfigOnly forbids it to
		// make one up: the config is all there is.
		email, _ := r.Config().Get("user.email")
		name, _ := r.Config().Get("user.name")
		add(name, email)
	}
	return out
}

// readAuthors reads the authors of the newest n commits reachable from
// tip, through mm, one per address, those with the most commits first.
func readAuthors(ctx context.Context, r *repo.Repository, mm *mailmap.Map, tip []byte, n int) ([]Person, error) {
	cat, err := r.CatFile()
	if err != nil {
		return nil, err
	}
	type count struct {
		Person
		n int
	}
	byEmail := map[string]*count{}
	add := func(o catfile.Object) {
		cm, err := object.ParseCommit(o.Data)
		if err != nil {
			return
		}
		enc := string(cm.Encoding)
		name, _ := textenc.Decode(cm.Author.Name, enc)
		email, _ := textenc.Decode(cm.Author.Email, enc)
		name, email = mm.Lookup(name, email)
		if email == "" {
			return
		}
		key := strings.ToLower(email)
		c := byEmail[key]
		if c == nil {
			c = &count{Person: Person{Name: name, Email: email}}
			byEmail[key] = c
		}
		c.n++
	}
	if ids := graphRecent(r, tip, n); ids != nil {
		// In batches, so that other readers of the process wait little.
		for i := 0; i < len(ids) && err == nil; i += authorBatch {
			if err = ctx.Err(); err != nil {
				break
			}
			err = cat.Read(ids[i:min(i+authorBatch, len(ids))], func(_ int, o catfile.Object) error {
				if o.Type == "commit" {
					add(o)
				}
				return nil
			})
		}
	} else {
		err = walkRecent(ctx, cat, tip, n, add)
	}
	counts := make([]*count, 0, len(byEmail))
	for _, c := range byEmail {
		counts = append(counts, c)
	}
	slices.SortFunc(counts, func(a, b *count) int {
		return cmp.Or(cmp.Compare(b.n, a.n), strings.Compare(a.Name, b.Name), strings.Compare(a.Email, b.Email))
	})
	out := make([]Person, 0, min(len(counts), maxPeople))
	for _, c := range counts[:min(len(counts), maxPeople)] {
		out = append(out, c.Person)
	}
	return out, err
}

// graphRecent lists the newest n commits from tip by the commit-graph,
// without reading one; nil when the graph does not hold tip.
func graphRecent(r *repo.Repository, tip []byte, n int) [][]byte {
	g, err := r.CommitGraph()
	if err != nil || g == nil {
		return nil
	}
	pos, ok := history.Covered(g, [][]byte{tip})
	if !ok {
		return nil
	}
	var out [][]byte
	w := history.NewWalk(g, pos)
	for len(out) < n {
		p, _, ok := w.Next()
		if !ok {
			break
		}
		out = append(out, g.OID(p))
	}
	if w.Err() != nil {
		return nil
	}
	return out
}

// walkRecent reads up to n commits from tip, a generation of parents at a
// time, handing each to add: near enough the newest without a
// commit-graph to order them.
func walkRecent(ctx context.Context, cat *catfile.Process, tip []byte, n int, add func(catfile.Object)) error {
	seen := map[string]bool{string(tip): true}
	front := [][]byte{tip}
	for read := 0; len(front) > 0 && read < n; {
		if err := ctx.Err(); err != nil {
			return err
		}
		front = front[:min(len(front), n-read)]
		read += len(front)
		var next [][]byte
		err := cat.Read(front, func(_ int, o catfile.Object) error {
			if o.Type != "commit" {
				return nil
			}
			add(o)
			for _, h := range object.ParentIDs(o.Data) {
				id, err := hex.DecodeString(string(h))
				if err == nil && !seen[string(id)] {
					seen[string(id)] = true
					next = append(next, id)
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		front = next
	}
	return nil
}
