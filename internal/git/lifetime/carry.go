package lifetime

import (
	"fmt"
	"runtime/debug"
	"sync"
)

// Carried is a panic recovered in a goroutine and raised again in the one
// that waited for it, with the stack where it happened.
type Carried struct {
	Value any
	Stack string
}

func (c *Carried) String() string { return fmt.Sprint(c.Value) }

// Carrier takes a panic out of the goroutines a function starts back to
// the function's own, where whoever called it can recover it: a panic in
// a goroutine nothing recovers ends the app.
type Carrier struct {
	mu sync.Mutex
	p  *Carried
}

// Carry, deferred in such a goroutine, recovers a panic in it and keeps
// the first for Raise.
func (c *Carrier) Carry() {
	v := recover()
	if v == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.p == nil {
		c.p = &Carried{Value: v, Stack: string(debug.Stack())}
	}
}

// Raise panics again with the panic Carry kept, if any; call it once the
// goroutines have returned.
func (c *Carrier) Raise() {
	c.mu.Lock()
	p := c.p
	c.mu.Unlock()
	if p != nil {
		panic(p)
	}
}

// PanicOf is the Panic of v, just recovered by the caller's deferred
// function: one Carried keeps the stack where it happened, followed by
// where it was raised again.
func PanicOf(v any) Panic {
	stack := string(debug.Stack())
	if c, ok := v.(*Carried); ok {
		return Panic{Value: fmt.Sprint(c.Value), Stack: c.Stack + "\nraised again:\n" + stack}
	}
	return Panic{Value: fmt.Sprint(v), Stack: stack}
}
