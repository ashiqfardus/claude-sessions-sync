package hostagent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Sweep reports the state of the launchd user agent.
//
// NOT VERIFIED ON A MAC. This machine is Windows-only, so every darwin path in this
// package is unrun until CI or a real Mac exercises it. Do not claim macOS support
// on the strength of it compiling.
func Sweep() (Status, error) {
	s := Status{Mechanism: "launchd"}

	home, err := os.UserHomeDir()
	if err != nil {
		return s, err
	}
	plist := filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
	if _, err := os.Stat(plist); err != nil {
		s.Detail = "not registered"
		return s, nil
	}
	s.Installed = true
	s.Detail = plist

	// `launchctl list <label>` prints the last exit status when the job is loaded.
	if out, err := exec.Command(systemBinary("launchctl"), "list", Label).Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "LastExitStatus") {
				s.Detail = plist + ", " + strings.TrimSpace(line)
				break
			}
		}
	} else {
		s.Detail = plist + ", present but not loaded (launchctl bootstrap gui/$UID)"
	}
	return s, nil
}
