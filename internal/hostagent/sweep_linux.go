package hostagent

import (
	"os/exec"
	"strings"
)

// Sweep reports the state of the systemd user timer, falling back to crontab.
//
// NOT VERIFIED ON LINUX beyond CI. See the note in sweep_darwin.go.
func Sweep() (Status, error) {
	s := Status{Mechanism: "systemd"}

	// `is-active` exits non-zero for a unit that exists but is stopped or failed, so
	// using it alone reports a broken sweep as "not installed" - two very different
	// problems with two different fixes, and the stopped one is the one that silently
	// stops backing anything up. Ask for the load and active state instead.
	out, err := exec.Command(systemBinary("systemctl"), "--user", "show", Unit,
		"--property=LoadState", "--property=ActiveState", "--property=Result").Output()
	if err == nil {
		props := map[string]string{}
		for _, line := range strings.Split(string(out), "\n") {
			if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
				props[k] = v
			}
		}
		switch props["LoadState"] {
		case "loaded":
			s.Installed = true
			s.Detail = props["ActiveState"]
			if r := props["Result"]; r != "" && r != "success" {
				s.Detail += ", last result " + r
			}
			if props["ActiveState"] != "active" {
				s.Detail += " - the timer is installed but NOT running, so nothing is being archived"
			}
			s.Detail += lingerNote()
			return s, nil
		case "not-found", "":
			// Fall through to cron.
		default:
			s.Installed = true
			s.Detail = "load state " + props["LoadState"]
			return s, nil
		}
	}

	if out, err := exec.Command(systemBinary("crontab"), "-l").Output(); err == nil {
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

// lingerNote warns about the failure mode that is invisible until you need the data:
// a user timer does not run while the user is logged out unless lingering is on.
func lingerNote() string {
	out, err := exec.Command(systemBinary("loginctl"), "show-user", "--property=Linger").Output()
	if err != nil {
		return ""
	}
	if strings.Contains(string(out), "Linger=no") {
		return ", but lingering is off: it will not run while you are logged out (loginctl enable-linger)"
	}
	return ""
}
