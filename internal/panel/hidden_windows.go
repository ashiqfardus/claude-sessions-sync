package panel

import (
	"io/fs"
	"syscall"
)

// isHidden reports the Hidden attribute, which Explorer hides by (AppData, $Recycle.Bin).
// Windows lists it with the name, so this reads nothing more.
func isHidden(e fs.DirEntry) bool {
	info, err := e.Info()
	if err != nil {
		return false
	}
	a, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return ok && a.FileAttributes&syscall.FILE_ATTRIBUTE_HIDDEN != 0
}
