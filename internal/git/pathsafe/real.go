package pathsafe

import "path/filepath"

// Real returns path as the system knows it: absolute, with symlinks
// resolved and, on macOS, in the letter case and Unicode form the disk
// holds. Two spellings of one folder on a file system that ignores case
// are then one string, so whatever is keyed by a repository's path (its
// tab, its library entry, its trust, its watcher's root) agrees. Windows'
// filepath.EvalSymlinks already takes the case from the disk. It reads
// the disk, so it runs off the main thread.
func Real(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return spelled(real), nil
}
