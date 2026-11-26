//go:build !windows

package panel

import "io/fs"

// isHidden is false here: a leading dot is the only hidden mark.
func isHidden(fs.DirEntry) bool { return false }
