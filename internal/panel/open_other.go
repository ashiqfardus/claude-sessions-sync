//go:build !darwin && !windows

package panel

import (
	"os/exec"

	"github.com/ashiqfardus/claude-sessions-sync/internal/hostagent"
)

// SystemBinary looks in the system folders (/bin, /usr/bin, ...) before PATH.
func openCmd(path string) *exec.Cmd {
	return hostagent.Command(hostagent.SystemBinary("xdg-open"), path)
}
