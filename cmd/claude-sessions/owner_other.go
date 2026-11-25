//go:build !windows

package main

import (
	"os"
	"syscall"
)

// ownedByMe reports whether this user owns the file: a run as root on a shared /tmp
// never touches another user's folders. One with no owner to read is not ours.
func ownedByMe(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
