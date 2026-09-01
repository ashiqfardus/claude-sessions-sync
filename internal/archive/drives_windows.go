package archive

import (
	"syscall"
)

// driveRoots lists the filesystem roots worth probing.
//
// Probing A: through Z: with os.Stat is not free: a disconnected network drive can
// block for seconds per letter, and A:/B: can spin up floppy hardware on machines old
// enough to have it. GetLogicalDrives returns a bitmask of the letters that actually
// exist, which is one syscall instead of 26 filesystem round-trips.
//
// Called through syscall rather than golang.org/x/sys so the module keeps its
// zero-dependency promise.
func driveRoots() []string {
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetLogicalDrives")
	mask, _, _ := proc.Call()
	if mask == 0 {
		return nil
	}

	var roots []string
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		letter := rune('A' + i)
		if letter == 'A' || letter == 'B' {
			continue // historically floppies; never a sync target
		}
		roots = append(roots, string(letter)+`:\`)
	}
	return roots
}
