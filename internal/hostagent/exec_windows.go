package hostagent

import (
	"os/exec"
	"syscall"
)

// createNoWindow is CREATE_NO_WINDOW, which package syscall does not export.
const createNoWindow = 0x08000000

// hideWindow starts a console child with no console window. CREATE_NO_WINDOW is ignored
// for GUI programs; HideWindow is their first ShowWindow, so a GUI child that should be
// seen must clear it (the panel's browser opener does).
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}
