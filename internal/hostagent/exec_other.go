//go:build !windows

package hostagent

import "os/exec"

// hideWindow does nothing: only Windows gives a console child its own window.
func hideWindow(*exec.Cmd) {}
