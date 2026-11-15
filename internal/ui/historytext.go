package ui

import (
	"bytes"
	"encoding/binary"

	"ramus/internal/git/history"
)

// historyText is the one-line text of the commits history showed, by
// object id, kept across loads: a commit's text is read once while it
// stays within the budget, whichever row it moves to as commits land
// above it or history is laid out anew.
type historyText struct {
	// chunks are the chunks read, by slot; nil for a free slot.
	chunks []*history.TextChunk
	used   []uint64 // the frame each slot was last used in
	gens   []int    // the gen each slot was read in
	free   []uint32
	bytes  int
	// at maps an object id's first 8 bytes to its slot<<8 | place in the
	// chunk: pointer-free, so the collector never scans it however many
	// commits it holds. A lookup checks the whole id.
	at map[uint64]uint32
	// pending are the commits being read, by the same key.
	pending map[uint64]struct{}
	// gen numbers the times the text went stale: text read before the
	// latest is shown until read again.
	gen int
	// names is the number of the mailmap the text named authors through.
	names int
}

func textKey(id []byte) uint64 {
	var k [8]byte
	copy(k[:], id)
	return binary.LittleEndian.Uint64(k[:])
}

func (x *historyText) find(id []byte) (slot uint32, k int, ok bool) {
	v, ok := x.at[textKey(id)]
	if !ok {
		return 0, 0, false
	}
	slot, k = v>>8, int(v&0xff)
	return slot, k, bytes.Equal(x.chunks[slot].OID(k), id)
}

// lookup returns the chunk holding commit id's text and its place in it.
func (x *historyText) lookup(id []byte) (*history.TextChunk, int, bool) {
	slot, k, ok := x.find(id)
	if !ok {
		return nil, 0, false
	}
	return x.chunks[slot], k, true
}

// use marks commit id's chunk used in frame, for it to stay.
func (x *historyText) use(id []byte, frame uint64) {
	if slot, _, ok := x.find(id); ok {
		x.used[slot] = frame
	}
}

// wanted reports whether commit id's text is to be read: it has none, or
// stale, and none is being read.
func (x *historyText) wanted(id []byte) bool {
	if slot, _, ok := x.find(id); ok && x.gens[slot] == x.gen {
		return false
	}
	_, ok := x.pending[textKey(id)]
	return !ok
}

func (x *historyText) ask(ids [][]byte) {
	if x.pending == nil {
		x.pending = map[uint64]struct{}{}
	}
	for _, id := range ids {
		x.pending[textKey(id)] = struct{}{}
	}
}

// add keeps ch, read for ids in gen, and drops the chunks used longest
// ago past budget; it reports whether it dropped any.
func (x *historyText) add(gen int, ids [][]byte, ch *history.TextChunk, frame uint64, budget int) bool {
	for _, id := range ids {
		delete(x.pending, textKey(id))
	}
	if ch == nil || ch.Len() == 0 {
		return false
	}
	if x.at == nil {
		x.at = map[uint64]uint32{}
	}
	var slot uint32
	if n := len(x.free); n > 0 {
		slot, x.free = x.free[n-1], x.free[:n-1]
		x.chunks[slot], x.used[slot], x.gens[slot] = ch, frame, gen
	} else {
		slot = uint32(len(x.chunks))
		x.chunks, x.used, x.gens = append(x.chunks, ch), append(x.used, frame), append(x.gens, gen)
	}
	for k := range ch.Len() {
		x.at[textKey(ch.OID(k))] = slot<<8 | uint32(k)
	}
	x.bytes += ch.Bytes()
	dropped := false
	for x.bytes > budget && len(x.chunks)-len(x.free) > 1 {
		oldest, at := -1, ^uint64(0)
		for s, c := range x.chunks {
			if c != nil && uint32(s) != slot && x.used[s] < at {
				oldest, at = s, x.used[s]
			}
		}
		x.drop(uint32(oldest))
		dropped = true
	}
	return dropped
}

func (x *historyText) drop(slot uint32) {
	ch := x.chunks[slot]
	for k := range ch.Len() {
		key := textKey(ch.OID(k))
		if x.at[key] == slot<<8|uint32(k) {
			delete(x.at, key)
		}
	}
	x.bytes -= ch.Bytes()
	x.chunks[slot] = nil
	x.free = append(x.free, slot)
}

// stale has all text read again as it is shown, as when the mailmap may
// name authors otherwise; until then the text there was shows, for the
// rows not to blink.
func (x *historyText) stale() { x.gen++ }

// kept returns the chunks held and their bytes, for tests.
func (x *historyText) kept() (n, size int) {
	return len(x.chunks) - len(x.free), x.bytes
}
