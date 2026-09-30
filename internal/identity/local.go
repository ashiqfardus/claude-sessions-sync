package identity

import (
	"path/filepath"
	"runtime"
)

// Local reports whether p is a path on this machine's own disks that is safe to look
// at.
//
// SECURITY: project paths come from the archive and from transcripts other machines
// wrote, so they are untrusted. On Windows even os.Stat on \\host\share connects to
// that host over SMB and hands it the user's NTLM hash, and a device path (\\.\ or
// \\?\) reaches things that are not files at all. Only absolute paths on a drive
// letter, or rooted at / elsewhere, are ever opened. Folders the user names on the
// command line are theirs to choose and are not filtered by this.
func Local(p string) bool {
	if !filepath.IsAbs(p) {
		return false
	}
	if runtime.GOOS == "windows" {
		v := filepath.VolumeName(p)
		return len(v) == 2 && v[1] == ':'
	}
	return true
}
