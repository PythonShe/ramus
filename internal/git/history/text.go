package history

import (
	"context"
	"time"
	"unicode/utf8"

	"ramus/internal/git/catfile"
	"ramus/internal/git/mailmap"
	"ramus/internal/git/object"
	"ramus/internal/git/redact"
	"ramus/internal/git/textenc"
)

// TextChunkRows is the most commits whose text loads as one unit: one
// batch through cat-file, one allocation.
const TextChunkRows = 256

// TextChunk holds the one-line text of up to TextChunkRows commits, by
// their place k in the chunk: subject, author name and email, in one
// buffer, and
// author times, with the commits' object ids. It is immutable once
// loaded, so the goroutine that loads it hands it to the UI as it is, and
// it is the text of its commits whichever rows they are in.
type TextChunk struct {
	// oids holds the object ids, hashSize bytes each.
	oids     []byte
	hashSize int
	buf      []byte
	// ends holds, per commit, the end of its subject, of its author, of
	// its merged parents and of its author's email in buf.
	ends  []uint32
	times []int64
	zones []int16
	// odd marks commits whose text was not valid in its encoding, shown
	// with escapes.
	odd []bool
}

// Len returns the number of commits the chunk holds.
func (c *TextChunk) Len() int { return len(c.times) }

// Bytes returns the memory the chunk holds, for cache budgets.
func (c *TextChunk) Bytes() int {
	return cap(c.oids) + cap(c.buf) + cap(c.ends)*4 + cap(c.times)*8 + cap(c.zones)*2 + cap(c.odd) + 64
}

// OID returns the object id of commit k.
func (c *TextChunk) OID(k int) []byte { return c.oids[k*c.hashSize : (k+1)*c.hashSize] }

// Row returns the subject, author name and author time of commit k, as
// copies of the chunk's text.
func (c *TextChunk) Row(k int) (subject, author string, when time.Time) {
	start := uint32(0)
	if k > 0 {
		start = c.ends[textParts*k-1]
	}
	s := c.buf[start:c.ends[textParts*k]]
	a := c.buf[c.ends[textParts*k]:c.ends[textParts*k+1]]
	loc := time.FixedZone("", int(c.zones[k])*60)
	return string(s), string(a), time.Unix(c.times[k], 0).In(loc)
}

// Odd reports whether commit k's subject or author was not valid text in
// the commit's encoding: its bytes are then shown as \xNN escapes.
func (c *TextChunk) Odd(k int) bool { return c.odd[k] }

// Merged returns the abbreviated parents of commit k when it is a merge,
// as "1a2b3c4 and 5d6e7f8", and "" for a commit with one parent or none.
func (c *TextChunk) Merged(k int) string {
	return string(c.buf[c.ends[textParts*k+1]:c.ends[textParts*k+2]])
}

// Email returns the email of commit k's author, as the mailmap maps it.
func (c *TextChunk) Email(k int) string {
	return string(c.buf[c.ends[textParts*k+2]:c.ends[textParts*k+3]])
}

// maxSubject bounds the bytes of a subject a chunk keeps: a row shows one
// line of it, and a message of one long paragraph would otherwise be kept
// whole and copied for every row drawn.
const maxSubject = 4 << 10

// clip cuts s to at most n bytes, on a rune boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// textParts is how many ends each commit has in a chunk's buffer.
const textParts = 4

// mergedShort is how many hex digits name a merged parent.
const mergedShort = 7

// LoadText reads the text of the commits ids, at most TextChunkRows of
// them, all of one hash size, through a cat-file process. Authors are
// named as mm maps them, which may be nil, as git log names them through
// the mailmap. Cancelling ctx ends the read with ctx's error.
func LoadText(ctx context.Context, p *catfile.Process, ids [][]byte, mm *mailmap.Map) (*TextChunk, error) {
	n := len(ids)
	c := &TextChunk{
		buf:   make([]byte, 0, n*64),
		ends:  make([]uint32, 0, textParts*n),
		times: make([]int64, 0, n),
		zones: make([]int16, 0, n),
		odd:   make([]bool, 0, n),
	}
	if n > 0 {
		c.hashSize = len(ids[0])
		c.oids = make([]byte, 0, n*c.hashSize)
		for _, id := range ids {
			c.oids = append(c.oids, id...)
		}
	}
	err := p.ReadContext(ctx, ids, func(i int, o catfile.Object) error {
		var cm object.Commit
		if o.Type == "commit" {
			cm, _ = object.ParseCommit(o.Data)
		}
		enc := string(cm.Encoding)
		subject, ok1 := textenc.Decode(object.Subject(cm.Message), enc)
		author, ok2 := textenc.Decode(cm.Author.Name, enc)
		email, _ := textenc.Decode(cm.Author.Email, enc)
		if mm.Len() > 0 {
			author, email = mm.Lookup(author, email)
		}
		// The text is only shown: without the controls and bidi overrides
		// a commit could carry to disguise its subject or its author.
		subject, author, email = redact.Control(subject), redact.Control(author), redact.Control(email)
		subject = clip(subject, maxSubject)
		c.odd = append(c.odd, !ok1 || !ok2)
		c.buf = append(c.buf, subject...)
		c.ends = append(c.ends, uint32(len(c.buf)))
		c.buf = append(c.buf, author...)
		c.ends = append(c.ends, uint32(len(c.buf)))
		if cm.Parents > 1 {
			c.buf = appendMerged(c.buf, o.Data)
		}
		c.ends = append(c.ends, uint32(len(c.buf)))
		c.buf = append(c.buf, email...)
		c.ends = append(c.ends, uint32(len(c.buf)))
		c.times = append(c.times, cm.Author.When)
		c.zones = append(c.zones, cm.Author.Offset)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return c, nil
}

// appendMerged appends the abbreviated parents named in a commit's header,
// joined as a sentence lists them.
func appendMerged(buf, data []byte) []byte {
	ids := object.ParentIDs(data)
	for k, id := range ids {
		switch {
		case k == len(ids)-1 && k > 0:
			buf = append(buf, " and "...)
		case k > 0:
			buf = append(buf, ", "...)
		}
		buf = append(buf, id[:min(len(id), mergedShort)]...)
	}
	return buf
}
