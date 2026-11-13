package main

import (
	"fmt"
	"math/rand/v2"
	"strings"
)

// generate returns an old and a new text of about size bytes each, of
// source-like lines: the new one changes one line in fifty, deletes and
// inserts blocks, the way a large generated file or a vendored library
// changes between versions. The same seed gives the same texts.
func generate(size int) (old, new string) {
	rng := rand.New(rand.NewPCG(7, 11))
	words := []string{"static", "int", "return", "struct", "const", "void", "unsigned", "if", "else", "for",
		"buffer", "length", "offset", "device", "driver", "config", "status", "error", "value", "index"}
	line := func(i int) string {
		var b strings.Builder
		b.WriteString(strings.Repeat("\t", rng.IntN(3)))
		for n := 3 + rng.IntN(8); n > 0; n-- {
			b.WriteString(words[rng.IntN(len(words))])
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "/* %d */\n", i)
		return b.String()
	}
	var o, n strings.Builder
	o.Grow(size + 1024)
	n.Grow(size + 1024)
	for i := 0; o.Len() < size; i++ {
		l := line(i)
		o.WriteString(l)
		switch r := rng.IntN(1000); {
		case r < 20: // changed
			n.WriteString(strings.Replace(l, " ", "  ", 1))
		case r < 22: // deleted
		case r < 24: // a block inserted
			for k := 0; k < 5; k++ {
				n.WriteString(line(-i))
			}
			n.WriteString(l)
		default:
			n.WriteString(l)
		}
	}
	return o.String(), n.String()
}
