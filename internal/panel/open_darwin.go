package panel

import (
	"os/exec"

	"github.com/ashiqfardus/claude-sessions-sync/internal/hostagent"
)

func openCmd(path string) *exec.Cmd { return hostagent.Command("/usr/bin/open", path) }
