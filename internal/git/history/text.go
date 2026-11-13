package history

import (
	"bytes"
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
	// ends holds, per row, the end of its subject, of its author and of
	// its merged parents in buf.
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
		start = c.ends[3*k-1]
	}
	s := c.buf[start:c.ends[3*k]]
	a := c.buf[c.ends[3*k]:c.ends[3*k+1]]
	loc := time.FixedZone("", int(c.zones[k])*60)
	return string(s), string(a), time.Unix(c.times[k], 0).In(loc)
}

// Merged returns the abbreviated parents of row i when it is a merge, as
// "1a2b3c4 and 5d6e7f8", and "" for a commit with one parent or none.
func (c *TextChunk) Merged(i int) string {
	k := i - c.First
	return string(c.buf[c.ends[3*k+1]:c.ends[3*k+2]])
}

// mergedShort is how many hex digits name a merged parent.
const mergedShort = 7

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
		ends:  make([]uint32, 0, 3*n),
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
		if cm.Parents > 1 {
			c.buf = appendMerged(c.buf, o.Data, cm.Parents)
		}
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
// n of them, joined as a sentence lists them.
func appendMerged(buf, data []byte, n int) []byte {
	k := 0
	for len(data) > 0 {
		line, rest, _ := bytes.Cut(data, []byte{'\n'})
		if len(line) == 0 {
			break // the end of the header
		}
		if id, ok := bytes.CutPrefix(line, []byte("parent ")); ok {
			switch {
			case k == n-1 && k > 0:
				buf = append(buf, " and "...)
			case k > 0:
				buf = append(buf, ", "...)
			}
			buf = append(buf, id[:min(len(id), mergedShort)]...)
			k++
		}
		data = rest
	}
	return buf
}
