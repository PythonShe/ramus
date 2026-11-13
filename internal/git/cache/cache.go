// Package cache is the least-recently-used cache every engine cache is
// built on: bounded by the bytes its values hold, not by their number,
// since one diff can outweigh ten thousand rows of text.
package cache

// Cache maps keys to values up to a byte budget, dropping the least
// recently used values to stay within it. It is not safe for use by
// several goroutines: each cache belongs to the goroutine that owns its
// data (the UI thread for what it shows), and others hand it values
// through that goroutine, so no one waits on a lock.
type Cache[K comparable, V any] struct {
	budget  int
	used    int
	size    func(V) int
	onEvict func(K, V)
	m       map[K]*entry[K, V]
	root    entry[K, V] // root.next is the most recent
}

type entry[K comparable, V any] struct {
	key        K
	val        V
	bytes      int
	prev, next *entry[K, V]
}

// New returns a cache of budget bytes; size tells what a value holds.
func New[K comparable, V any](budget int, size func(V) int) *Cache[K, V] {
	c := &Cache[K, V]{budget: budget, size: size, m: map[K]*entry[K, V]{}}
	c.root.next, c.root.prev = &c.root, &c.root
	return c
}

// OnEvict sets a function called with each value the cache drops or
// replaces, to release what it holds (a mapping, a file).
func (c *Cache[K, V]) OnEvict(fn func(K, V)) { c.onEvict = fn }

// Len returns the number of values held.
func (c *Cache[K, V]) Len() int { return len(c.m) }

// Bytes returns the bytes the values hold.
func (c *Cache[K, V]) Bytes() int { return c.used }

// Budget returns the byte budget.
func (c *Cache[K, V]) Budget() int { return c.budget }

// SetBudget changes the budget, dropping values to meet it.
func (c *Cache[K, V]) SetBudget(n int) {
	c.budget = n
	c.trim()
}

// Get returns the value for k and marks it the most recent.
func (c *Cache[K, V]) Get(k K) (V, bool) {
	e, ok := c.m[k]
	if !ok {
		var zero V
		return zero, false
	}
	c.unlink(e)
	c.pushFront(e)
	return e.val, true
}

// Peek returns the value for k without marking it.
func (c *Cache[K, V]) Peek(k K) (V, bool) {
	e, ok := c.m[k]
	if !ok {
		var zero V
		return zero, false
	}
	return e.val, true
}

// Put stores v for k as the most recent and drops the least recent values
// past the budget. A value larger than the whole budget is not kept, and
// Put returns false; the caller uses it once and lets it go.
func (c *Cache[K, V]) Put(k K, v V) bool {
	n := c.size(v)
	c.Remove(k)
	if n > c.budget {
		if c.onEvict != nil {
			c.onEvict(k, v)
		}
		return false
	}
	e := &entry[K, V]{key: k, val: v, bytes: n}
	c.m[k] = e
	c.pushFront(e)
	c.used += n
	c.trim()
	return true
}

// Remove drops the value for k.
func (c *Cache[K, V]) Remove(k K) {
	if e, ok := c.m[k]; ok {
		c.drop(e)
	}
}

// Clear drops every value.
func (c *Cache[K, V]) Clear() {
	for c.root.prev != &c.root {
		c.drop(c.root.prev)
	}
}

func (c *Cache[K, V]) trim() {
	for c.used > c.budget && c.root.prev != &c.root {
		c.drop(c.root.prev)
	}
}

func (c *Cache[K, V]) drop(e *entry[K, V]) {
	c.unlink(e)
	delete(c.m, e.key)
	c.used -= e.bytes
	if c.onEvict != nil {
		c.onEvict(e.key, e.val)
	}
}

func (c *Cache[K, V]) unlink(e *entry[K, V]) {
	e.prev.next, e.next.prev = e.next, e.prev
	e.prev, e.next = nil, nil
}

func (c *Cache[K, V]) pushFront(e *entry[K, V]) {
	e.prev, e.next = &c.root, c.root.next
	c.root.next.prev = e
	c.root.next = e
}
