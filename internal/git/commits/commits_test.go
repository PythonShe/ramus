package commits

import (
	"crypto/sha1"
	"encoding/binary"
	"slices"
	"testing"
)

func oid(i int) []byte {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], uint64(i))
	h := sha1.Sum(b[:])
	return h[:]
}

func TestStore(t *testing.T) {
	s := New(20)
	const n = 100_000
	snap := s.Snapshot()
	snapLen := 0
	for i := 0; i < n; i++ {
		id := s.ID(oid(i))
		if int(id) != i {
			t.Fatalf("id %d for %d", id, i)
		}
		var parents []uint32
		for k := 1; k <= i%4; k++ {
			parents = append(parents, s.ID(oid(i+k))) // named before added
		}
		if err := s.Add(id, parents, int64(i)); err != nil {
			t.Fatal(err)
		}
		if i == n/2 {
			snap, snapLen = s.Snapshot(), s.Len()
		}
	}
	for i := 0; i < n; i++ {
		id, ok := s.Lookup(oid(i))
		if !ok || int(id) != i || string(s.OID(id)) != string(oid(i)) {
			t.Fatalf("lookup %d: %d %v", i, id, ok)
		}
		ps, ok := s.Parents(id, nil)
		if !ok || len(ps) != i%4 || s.CommitTime(id) != int64(i) {
			t.Fatalf("commit %d: parents %v %v time %d", i, ps, ok, s.CommitTime(id))
		}
		for k, p := range ps {
			if string(s.OID(p)) != string(oid(i+k+1)) {
				t.Fatalf("commit %d parent %d", i, k)
			}
		}
	}
	if _, ok := s.Parents(uint32(s.Len()-1), nil); ok {
		t.Error("a parent never added has parents")
	}
	if _, ok := s.Lookup(oid(-1)); ok {
		t.Error("found an id never added")
	}
	if snap.Len() != snapLen || string(snap.OID(7)) != string(oid(7)) {
		t.Errorf("snapshot len %d", snap.Len())
	}
	if s.Bytes() <= 0 || s.Bytes() > 64<<20 {
		t.Errorf("bytes %d", s.Bytes())
	}
}

func TestParentListsDoNotStraddle(t *testing.T) {
	s := New(20)
	big := make([]uint32, MaxParents)
	for i := 0; i < 5; i++ {
		id := s.ID(oid(i))
		big[0] = id
		if err := s.Add(id, big, 0); err != nil {
			t.Fatal(err)
		}
		got, _ := s.Parents(id, nil)
		if len(got) != MaxParents || got[0] != id {
			t.Fatalf("commit %d: %d parents", i, len(got))
		}
	}
	if err := s.Add(0, make([]uint32, MaxParents+1), 0); err != ErrTooManyParents {
		t.Errorf("err %v", err)
	}
	if !slices.Equal(s.s.oids[0][:20], oid(0)) {
		t.Error("oid storage")
	}
}

func BenchmarkKernelSized(b *testing.B) {
	ids := make([][]byte, 1_500_000)
	for i := range ids {
		ids[i] = oid(i)
	}
	b.ResetTimer()
	for b.Loop() {
		s := New(20)
		var ps [2]uint32
		for i := range ids {
			id := s.ID(ids[i])
			p := ps[:0]
			if i+1 < len(ids) {
				p = append(p, s.ID(ids[i+1]))
			}
			s.Add(id, p, 0)
		}
		b.ReportMetric(float64(s.Bytes())/1e6, "MB")
	}
}
