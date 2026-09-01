//go:build !windows

package archive

// driveRoots is Windows-only: everywhere else there is a single filesystem root and
// synced folders live under the user's home directory, which detect() already covers.
func driveRoots() []string { return nil }
