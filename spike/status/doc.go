// Package status measures a status of a Chromium-size work tree (the
// generated 390k-file monorepo fixture) shown as a tree: git's own time,
// building the tree, and frames of ui.Outline with every folder closed and
// every folder open. It runs headless:
//
//	RAMUS_FIXTURES=~/Projects/ramus-fixtures taskpolicy -c background \
//	  go test ./spike/status -run Monorepo -v
package status
