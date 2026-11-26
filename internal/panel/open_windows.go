package panel

import (
	"os/exec"

	"github.com/ashiqfardus/claude-sessions-sync/internal/hostagent"
)

// rundll32 is a GUI program, so starting it opens no console window.
//
// HideWindow is cleared: rundll32 hands its show state to url.dll, which passes it to
// ShellExecute, so SW_HIDE could start a browser that was not running hidden.
func openCmd(path string) *exec.Cmd {
	cmd := hostagent.Command(hostagent.SystemBinary("rundll32"), "url.dll,FileProtocolHandler", path)
	cmd.SysProcAttr.HideWindow = false
	return cmd
}
