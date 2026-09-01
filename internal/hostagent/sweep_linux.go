package hostagent

import (
	"os/exec"
	"strings"
)

// Unit is the systemd user timer name.
const Unit = "claude-sessions-sync-sweep.timer"

// Sweep reports the state of the systemd user timer, falling back to crontab.
//
// NOT VERIFIED ON LINUX. See the note in sweep_darwin.go - this compiles here but
// has never run on the target OS.
func Sweep() (Status, error) {
	s := Status{Mechanism: "systemd"}

	if out, err := exec.Command("systemctl", "--user", "is-active", Unit).Output(); err == nil {
		s.Installed = true
		s.Detail = strings.TrimSpace(string(out))

		// A user timer does not run while the user is logged out unless lingering is
		// enabled. Silently not running is the worst possible failure for a backup
		// tool, so say it plainly.
		if out, err := exec.Command("loginctl", "show-user", "--property=Linger").Output(); err == nil {
			if strings.Contains(string(out), "Linger=no") {
				s.Detail += ", but lingering is off: the sweep will not run while you are logged out (loginctl enable-linger)"
			}
		}
		return s, nil
	}

	if out, err := exec.Command("crontab", "-l").Output(); err == nil {
		if strings.Contains(string(out), "claude-sessions") {
			s.Installed = true
			s.Mechanism = "cron"
			s.Detail = "crontab entry present"
			return s, nil
		}
	}

	s.Detail = "not registered"
	return s, nil
}
