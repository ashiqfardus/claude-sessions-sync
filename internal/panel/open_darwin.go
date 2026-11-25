package panel

import "os/exec"

func openCmd(path string) *exec.Cmd { return exec.Command("/usr/bin/open", path) }
