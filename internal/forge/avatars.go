package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The pictures of the people who made commits, as GitHub has them: who a
// commit's author, co-authors and committer are on the host, and their
// pictures, both read through gh, which is logged in. GitLab is not
// asked: glab names no account by a commit's address.

// ErrNoAvatars is a host whose people's pictures are not read.
var ErrNoAvatars = errors.New("forge: the pictures of people on this host are not read")

// ErrNotAPicture is an answer to Avatar that is neither PNG nor JPEG.
var ErrNotAPicture = errors.New("forge: the answer is not a PNG or JPEG picture")

const (
	// AvatarPixels is the width and height of the pictures asked for:
	// enough for a 32-point picture on a screen of twice the density.
	AvatarPixels = 64
	// CommitBatch is the most commits one query of CommitPeople names;
	// more are asked in several.
	CommitBatch = 50
	// MaxAvatar bounds the bytes of a picture: one AvatarPixels square is
	// a few KiB, so more is not one.
	MaxAvatar = 256 << 10
	// AvatarTimeout bounds one picture's download.
	AvatarTimeout = 15 * time.Second
	// coAuthors bounds the authors read of one commit.
	coAuthors = 10
)

// Person is someone a commit names, its author, a co-author or its
// committer, as the host knows them.
type Person struct {
	// Email is the address the commit gives, in lower case, and Name
	// the name beside it.
	Email, Name string
	// Login is the account the host ties the address to, "" for none.
	Login string
	// Avatar is the address of the account's picture, AvatarPixels
	// square, "" without an account. It is on the host's own picture
	// host (avatarURL), and Avatar downloads it.
	Avatar string
}

// CommitPeople asks GitHub for the people of commits shas, full commit
// ids, in r: each one's authors, co-authors among them, and committer,
// by commit id, an address once per commit. A commit the host does not
// have, as one not pushed, is left out. CommitBatch commits go in one
// query. A host the tool does not know (Knows) is not asked:
// ErrUnknownHost; a GitLab one answers ErrNoAvatars.
func (c *CLI) CommitPeople(ctx context.Context, r Repo, shas []string) (map[string][]Person, error) {
	if r.Kind != c.Kind {
		return nil, ErrNoCLI
	}
	if r.Kind != GitHub {
		return nil, ErrNoAvatars
	}
	if !c.Knows(r.Host) {
		return nil, ErrUnknownHost
	}
	for _, sha := range shas {
		if !hexID(sha) {
			return nil, fmt.Errorf("forge: %q is not a commit id", sha)
		}
	}
	owner, name, _ := strings.Cut(r.Path, "/")
	out := map[string][]Person{}
	for len(shas) > 0 {
		batch := shas[:min(len(shas), CommitBatch)]
		shas = shas[len(batch):]
		ans, err := c.run(ctx, "", r.Host, "api", "graphql", "--hostname", r.Host,
			"-f", "query="+peopleQuery(batch), "-f", "owner="+owner, "-f", "name="+name)
		if err != nil {
			return nil, err
		}
		if err := parsePeople(ans, r, batch, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// peopleQuery asks for the people of commits shas, each under an alias
// cN. The ids are checked hex, so they go in as they are.
func peopleQuery(shas []string) string {
	var b strings.Builder
	b.WriteString("query($owner:String!,$name:String!){repository(owner:$owner,name:$name){")
	for i, sha := range shas {
		fmt.Fprintf(&b, "c%d:object(oid:%q){...p}", i, sha)
	}
	fmt.Fprintf(&b, "}}fragment u on User{login avatarUrl(size:%d)}", AvatarPixels)
	fmt.Fprintf(&b, "fragment p on Commit{authors(first:%d){nodes{email name user{...u}}} committer{email name user{...u}}}", coAuthors)
	return b.String()
}

type ghActor struct {
	Email string `json:"email"`
	Name  string `json:"name"`
	User  *struct {
		Login  string `json:"login"`
		Avatar string `json:"avatarUrl"`
	} `json:"user"`
}

// parsePeople reads the answer to peopleQuery for shas into out. A
// picture's address that is not on r's picture host is dropped: it is
// downloaded with the user's login.
func parsePeople(ans []byte, r Repo, shas []string, out map[string][]Person) error {
	var a struct {
		graphQLError
		Data struct {
			Repository map[string]*struct {
				Authors *struct {
					Nodes []ghActor `json:"nodes"`
				} `json:"authors"`
				Committer *ghActor `json:"committer"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(ans, &a); err != nil {
		return fmt.Errorf("gh: %w", err)
	}
	// A commit the host lacks may come as an error beside the others'
	// answers; with no answer at all, the error is the query's.
	if a.Data.Repository == nil {
		if err := a.err("gh"); err != nil {
			return err
		}
		return nil
	}
	for i, sha := range shas {
		cm := a.Data.Repository["c"+strconv.Itoa(i)]
		if cm == nil || cm.Authors == nil && cm.Committer == nil {
			continue
		}
		var actors []ghActor
		if cm.Authors != nil {
			actors = cm.Authors.Nodes
		}
		if cm.Committer != nil {
			actors = append(actors, *cm.Committer)
		}
		people := []Person{}
		for _, ac := range actors {
			p := Person{Email: strings.ToLower(strings.TrimSpace(ac.Email)), Name: ac.Name}
			if p.Email == "" {
				continue
			}
			if ac.User != nil && ac.User.Login != "" && r.avatarURL(ac.User.Avatar) {
				p.Login, p.Avatar = ac.User.Login, ac.User.Avatar
			}
			if j := indexPerson(people, p.Email); j >= 0 {
				if people[j].Avatar == "" {
					people[j] = p
				}
				continue
			}
			people = append(people, p)
		}
		out[sha] = people
	}
	return nil
}

func indexPerson(people []Person, email string) int {
	for i, p := range people {
		if p.Email == email {
			return i
		}
	}
	return -1
}

// avatarURL reports a picture address that may be downloaded with the
// user's login to r's host: on GitHub's picture host for github.com, else
// on r's host itself or its avatars subdomain, as GitHub Enterprise
// serves them, over https, http only from a self-hosted host. It goes to
// gh as the endpoint, so it is an absolute URL and nothing else.
func (r Repo) avatarURL(s string) bool {
	u, err := url.Parse(s)
	if err != nil || u.User != nil || u.Opaque != "" || u.Port() != "" || u.Fragment != "" || !strings.HasPrefix(u.Path, "/") {
		return false
	}
	host := strings.ToLower(u.Host)
	switch {
	case r.Kind != GitHub:
		return false
	case r.Host == "github.com":
		return u.Scheme == "https" && host == "avatars.githubusercontent.com"
	case host != r.Host && host != "avatars."+r.Host:
		return false
	}
	return u.Scheme == "https" || u.Scheme == "http"
}

// Avatar downloads the picture at addr, an Avatar CommitPeople gave for
// r, through gh: PNG or JPEG bytes, at most MaxAvatar of them, within
// AvatarTimeout. Anything else fails, ErrTooLarge or ErrNotAPicture; a
// picture that is gone, ErrNotFound.
func (c *CLI) Avatar(ctx context.Context, r Repo, addr string) ([]byte, error) {
	if r.Kind != c.Kind {
		return nil, ErrNoCLI
	}
	if !c.Knows(r.Host) {
		return nil, ErrUnknownHost
	}
	if !r.avatarURL(addr) {
		return nil, fmt.Errorf("forge: %q is not a picture of %s", addr, r.Host)
	}
	b, err := c.runWith(ctx, "", r.Host, nil, MaxAvatar, AvatarTimeout, "api", addr)
	if err != nil {
		return nil, err
	}
	if !IsPicture(b) {
		return nil, ErrNotAPicture
	}
	return b, nil
}

// IsPicture reports PNG or JPEG bytes, by their signature.
func IsPicture(b []byte) bool {
	return bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")) || bytes.HasPrefix(b, []byte("\xff\xd8\xff"))
}
