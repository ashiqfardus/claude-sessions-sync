package panel

import (
	"os/exec"

	"github.com/ashiqfardus/claude-sessions-sync/internal/hostagent"
)

// rundll32 is a GUI program, so starting it opens no console window.
func openCmd(path string) *exec.Cmd {
	return exec.Command(hostagent.SystemBinary("rundll32"), "url.dll,FileProtocolHandler", path)
}
