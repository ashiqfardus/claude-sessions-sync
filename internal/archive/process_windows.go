package archive

import "syscall"

// processAlive reports whether pid is a running process.
//
// OpenProcess alone is not enough: it also succeeds for a process that has exited
// while something still holds a handle to it.
func processAlive(pid int) bool {
	const processQueryLimitedInformation = 0x1000
	const stillActive = 259
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		// Access denied means it exists; anything else means it does not.
		return err == syscall.ERROR_ACCESS_DENIED
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return true
	}
	return code == stillActive
}
