package cache

import (
	"math/rand/v2"
	"testing"
)

func TestBudget(t *testing.T) {
	c := New[int, []byte](100, func(b []byte) int { return len(b) })
	var evicted []int
	c.OnEvict(func(k int, _ []byte) { evicted = append(evicted, k) })
	for k := 0; k < 4; k++ {
		c.Put(k, make([]byte, 30))
	}
	if c.Len() != 3 || c.Bytes() != 90 || len(evicted) != 1 || evicted[0] != 0 {
		t.Fatalf("len %d bytes %d evicted %v", c.Len(), c.Bytes(), evicted)
	}
	c.Get(1) // 1 is now the most recent; 2 goes next
	c.Put(4, make([]byte, 30))
	if _, ok := c.Peek(2); ok {
		t.Error("kept the least recent")
	}
	if _, ok := c.Peek(1); !ok {
		t.Error("dropped the most recently used")
	}
	if c.Put(5, make([]byte, 101)) {
		t.Error("kept a value over the budget")
	}
	c.Put(1, make([]byte, 10)) // replacing releases the old value
	if c.Bytes() != 70 {
		t.Errorf("bytes %d after replace", c.Bytes())
	}
	c.SetBudget(20)
	if c.Bytes() > 20 {
		t.Errorf("bytes %d over a lowered budget", c.Bytes())
	}
	c.Clear()
	if c.Len() != 0 || c.Bytes() != 0 {
		t.Error("clear")
	}
}

// The cache's own accounting holds under any sequence of operations.
func TestRandom(t *testing.T) {
	c := New[int, int](1000, func(v int) int { return v })
	live := map[int]int{}
	c.OnEvict(func(k, v int) {
		if live[k] != v {
			t.Fatalf("evicted %d=%d, held %d", k, v, live[k])
		}
		delete(live, k)
	})
	r := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < 100_000; i++ {
		k := r.IntN(50)
		switch r.IntN(4) {
		case 0, 1:
			v := r.IntN(300) + 1
			c.Remove(k)
			live[k] = v
			c.Put(k, v)
		case 2:
			c.Get(k)
		case 3:
			c.Remove(k)
		}
		sum := 0
		for _, v := range live {
			sum += v
		}
		if sum != c.Bytes() || len(live) != c.Len() || c.Bytes() > c.Budget() {
			t.Fatalf("step %d: bytes %d, live %d in %d", i, c.Bytes(), sum, len(live))
		}
	}
}
