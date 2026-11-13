package ui

import "strings"

// refRow is a row of the branch or tag list: a folder of names sharing a
// prefix up to a "/", or one name.
type refRow struct {
	depth int
	// label is the last part of the name, or the folder's.
	label string
	// folder is a folder's whole prefix, "" for a name.
	folder string
	// idx is the name's index, -1 for a folder.
	idx int
}

// groupRefs lays names (sorted, prefix already cut) out as rows grouped by
// "/". A folder in closed shows without what is in it. With a filter, only
// the names containing it (in any case) show, every folder open.
func groupRefs(names []string, filter string, closed map[string]bool) []refRow {
	filter = strings.ToLower(filter)
	var rows []refRow
	var prev []string // the folders of the last name looked at
	for i, name := range names {
		if filter != "" && !strings.Contains(strings.ToLower(name), filter) {
			continue
		}
		parts := strings.Split(name, "/")
		dirs := parts[:len(parts)-1]
		// shut is the first of its folders that is closed, -1 for none.
		shut := -1
		for d := range dirs {
			if filter == "" && closed[strings.Join(dirs[:d+1], "/")] {
				shut = d
				break
			}
		}
		same := 0
		for same < len(dirs) && same < len(prev) && dirs[same] == prev[same] {
			same++
		}
		for d := same; d < len(dirs) && (shut < 0 || d <= shut); d++ {
			rows = append(rows, refRow{depth: d, label: dirs[d], folder: strings.Join(dirs[:d+1], "/"), idx: -1})
		}
		prev = dirs
		if shut < 0 {
			rows = append(rows, refRow{depth: len(dirs), label: parts[len(parts)-1], idx: i})
		}
	}
	return rows
}
