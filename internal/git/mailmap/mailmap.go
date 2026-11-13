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
	name1, email1, rest, ok := nameAndEmail(line)
	if !ok {
		return
	}
	name2, email2, _, ok := nameAndEmail(rest)
	if !ok {
		// "Proper Name <commit@email>": the address is the key and stays.
		m.add("", email1, name1, "")
		return
	}
	m.add(name2, email2, name1, email1)
}

func nameAndEmail(s string) (name, email, rest string, ok bool) {
	lt := strings.IndexByte(s, '<')
	if lt < 0 {
		return "", "", "", false
	}
	gt := strings.IndexByte(s[lt+1:], '>')
	if gt < 0 {
		return "", "", "", false
	}
	return strings.TrimSpace(s[:lt]), s[lt+1 : lt+1+gt], s[lt+1+gt+1:], true
}

func (m *Map) add(oldName, oldEmail, newName, newEmail string) {
	if m.byEmail == nil {
		m.byEmail = map[string]*entry{}
	}
	key := strings.ToLower(oldEmail)
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
	n := strings.ToLower(oldName)
	p := e.names[n]
	if p == nil {
		p = &person{}
		e.names[n] = p
	}
	p.merge(newName, newEmail)
}

// Len returns the number of addresses m maps.
func (m *Map) Len() int { return len(m.byEmail) }

// Lookup returns the canonical name and address of an identity; one m
// does not name comes back as it is. Names and addresses match ignoring
// case.
func (m *Map) Lookup(name, email string) (string, string) {
	if m == nil || len(m.byEmail) == 0 {
		return name, email
	}
	e := m.byEmail[strings.ToLower(email)]
	if e == nil {
		return name, email
	}
	p := &e.def
	if q := e.names[strings.ToLower(name)]; q != nil {
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
