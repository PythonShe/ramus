// Package mailmap reads git's .mailmap files, which give the people in
// a history one name and address each, and maps identities through them
// as git does.
package mailmap

import (
	"bytes"
	"strings"
)

// Map maps commit identities to canonical ones. The zero Map maps
// nothing.
type Map struct {
	byEmail map[string]*entry
}

type entry struct {
	// def applies to any name at this address; names to one name.
	def   person
	names map[string]*person
}

type person struct{ name, email string }

func (p *person) merge(name, email string) {
	if name != "" {
		p.name = name
	}
	if email != "" {
		p.email = email
	}
}

// Add reads the lines of a mailmap file into m; a later line for the
// same identity overrides what an earlier one set, as in git.
func (m *Map) Add(data []byte) {
	for line := range bytes.SplitSeq(data, []byte{'\n'}) {
		m.addLine(string(bytes.TrimSuffix(line, []byte{'\r'})))
	}
}

// addLine follows git's read_mailmap_line: a proper name and address,
// then the commit's name and address; text past the last '>' is ignored.
func (m *Map) addLine(line string) {
	if strings.HasPrefix(line, "#") {
		return
	}
	// The first address may not be empty, the second may.
	name1, email1, rest, ok := nameAndEmail(line, false)
	if !ok {
		return
	}
	name2, email2, _, ok := nameAndEmail(rest, true)
	if !ok {
		// "Proper Name <commit@email>": the address is the key and stays.
		m.add("", email1, name1, "")
		return
	}
	m.add(name2, email2, name1, email1)
}

func nameAndEmail(s string, allowEmpty bool) (name, email, rest string, ok bool) {
	lt := strings.IndexByte(s, '<')
	if lt < 0 {
		return "", "", "", false
	}
	gt := strings.IndexByte(s[lt+1:], '>')
	if gt < 0 || gt == 0 && !allowEmpty {
		return "", "", "", false
	}
	return strings.TrimSpace(s[:lt]), s[lt+1 : lt+1+gt], s[lt+1+gt+1:], true
}

func (m *Map) add(oldName, oldEmail, newName, newEmail string) {
	if m.byEmail == nil {
		m.byEmail = map[string]*entry{}
	}
	key := fold(oldEmail)
	e := m.byEmail[key]
	if e == nil {
		e = &entry{}
		m.byEmail[key] = e
	}
	if oldName == "" {
		e.def.merge(newName, newEmail)
		return
	}
	if e.names == nil {
		e.names = map[string]*person{}
	}
	// A later line for the same name and address replaces the earlier,
	// where one for the address alone adds to it.
	e.names[fold(oldName)] = &person{newName, newEmail}
}

// fold lower-cases ASCII letters alone: git matches names and addresses
// with strcasecmp, which leaves other letters as they are.
func fold(s string) string {
	for i := 0; i < len(s); i++ {
		if 'A' <= s[i] && s[i] <= 'Z' {
			b := []byte(s)
			for j := i; j < len(b); j++ {
				if 'A' <= b[j] && b[j] <= 'Z' {
					b[j] += 'a' - 'A'
				}
			}
			return string(b)
		}
	}
	return s
}

// Len returns the number of addresses m maps.
func (m *Map) Len() int {
	if m == nil {
		return 0
	}
	return len(m.byEmail)
}

// Lookup returns the canonical name and address of an identity; one m
// does not name comes back as it is. Names and addresses match ignoring
// the case of ASCII letters.
func (m *Map) Lookup(name, email string) (string, string) {
	if m == nil || len(m.byEmail) == 0 {
		return name, email
	}
	e := m.byEmail[fold(email)]
	if e == nil {
		return name, email
	}
	p := &e.def
	if q := e.names[fold(name)]; q != nil {
		p = q
	} else if p.name == "" && p.email == "" {
		return name, email
	}
	if p.name != "" {
		name = p.name
	}
	if p.email != "" {
		email = p.email
	}
	return name, email
}
