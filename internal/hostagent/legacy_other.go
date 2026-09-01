//go:build !windows

package hostagent

// LegacySweep and RemoveLegacySweep exist only on Windows: the PowerShell predecessor
// this tool replaces never ran anywhere else, so there is nothing to detect.
func LegacySweep() (string, bool) { return "", false }

func RemoveLegacySweep() error { return nil }
