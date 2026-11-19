//go:build !darwin

package pathsafe

// spelled is real: Linux file systems mostly keep case apart, and
// Windows' EvalSymlinks already took the case from the disk.
func spelled(real string) string { return real }
