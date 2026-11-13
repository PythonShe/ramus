package history

import (
	"time"

	"ramus/internal/git/catfile"
	"ramus/internal/git/object"
)

// TextChunkRows is the number of consecutive rows whose text loads as one
// unit: one batch through cat-file, one allocation.
const TextChunkRows = 256

// TextChunk holds the one-line text of TextChunkRows consecutive rows:
// subject and author name, in one buffer, and author times. It is
// immutable once loaded, so the goroutine that loads it hands it to the UI
// as it is.
type TextChunk struct {
	First int // the first row
	buf   []byte
	// ends holds, per row, the end of its subject and of its author in buf.
	ends  []uint32
	times []int64
	zones []int16
}

// Len returns the number of rows the chunk holds.
func (c *TextChunk) Len() int { return len(c.times) }

// Bytes returns the memory the chunk holds, for cache budgets.
func (c *TextChunk) Bytes() int {
	return cap(c.buf) + cap(c.ends)*4 + cap(c.times)*8 + cap(c.zones)*2 + 64
}

// Row returns the subject, author name and author time of row i, which
// must be within the chunk. The strings share the chunk's buffer.
func (c *TextChunk) Row(i int) (subject, author string, when time.Time) {
	k := i - c.First
	start := uint32(0)
	if k > 0 {
		start = c.ends[2*k-1]
	}
	s := c.buf[start:c.ends[2*k]]
	a := c.buf[c.ends[2*k]:c.ends[2*k+1]]
	loc := time.FixedZone("", int(c.zones[k])*60)
	return string(s), string(a), time.Unix(c.times[k], 0).In(loc)
}

// LoadText reads the text of rows first to first+n-1 through a cat-file
// process. commit gives a row's commit number and oid that number's object
// id: a commit-graph position and Graph.OID, or a commit store number and
// its snapshot's OID.
func LoadText(p *catfile.Process, oid func(id uint32) []byte, first, n int, commit func(row int) uint32) (*TextChunk, error) {
	ids := make([][]byte, n)
	for i := range ids {
		ids[i] = oid(commit(first + i))
	}
	c := &TextChunk{
		First: first,
		buf:   make([]byte, 0, n*64),
		ends:  make([]uint32, 0, 2*n),
		times: make([]int64, 0, n),
		zones: make([]int16, 0, n),
	}
	err := p.Read(ids, func(i int, o catfile.Object) error {
		var cm object.Commit
		if o.Type == "commit" {
			cm, _ = object.ParseCommit(o.Data)
		}
		c.buf = append(c.buf, object.Subject(cm.Message)...)
		c.ends = append(c.ends, uint32(len(c.buf)))
		c.buf = append(c.buf, cm.Author.Name...)
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
