package hostagent

import (
	"os/exec"
	"strings"
)

// Sweep reports the state of this profile's systemd user timer, falling back to
// crontab.
//
// NOT VERIFIED ON LINUX beyond CI. See the note in sweep_darwin.go.
func Sweep(j Job) (Status, error) {
	s := Status{Mechanism: "systemd"}

	// `is-active` exits non-zero for a unit that exists but is stopped or failed, so
	// using it alone reports a broken sweep as "not installed" - two very different
	// problems with two different fixes, and the stopped one is the one that silently
	// stops backing anything up. Ask for the load and active state instead.
	out, err := Command(systemBinary("systemctl"), "--user", "show", j.SystemdTimerName(),
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
			s.Detail = j.SystemdTimerName() + " " + props["ActiveState"]
			if r := props["Result"]; r != "" && r != "success" {
				s.Detail += ", last result " + r
			}
			if props["ActiveState"] != "active" {
				s.Problem = "the timer is installed but NOT running, so nothing is being archived"
			}
			s.Detail += lingerNote()
			return s, nil
		case "not-found", "":
			// Fall through to cron.
		default:
			s.Installed = true
			s.Detail = "load state " + props["LoadState"]
			s.Problem = "the timer is registered but systemd cannot load it"
			return s, nil
		}
	}

	if out, err := Command(systemBinary("crontab"), "-l").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if ownsCronLine(j, line) {
				s.Installed = true
				s.Mechanism = "cron"
				s.Detail = "crontab entry present"
				// An entry is not a schedule: WSL, and many containers, never start a
				// cron daemon, so the line sits there and nothing ever runs it.
				if _, err := exec.LookPath("pgrep"); err == nil &&
					strings.TrimSpace(runOut("pgrep", "-x", "cron|crond|cronie|busybox")) == "" {
					s.Problem = "no cron daemon is running, so the entry never fires (on WSL: sudo service cron start)"
				}
				return s, nil
			}
		}
	}

	s.Detail = "not registered"
	return s, nil
}

// lingerNote warns about the failure mode that is invisible until you need the data:
// a user timer does not run while the user is logged out unless lingering is on.
//
// The user is named explicitly: older systemd rejects `show-user` without one.
func lingerNote() string {
	u := strings.TrimSpace(runOut("id", "-un"))
	if u == "" {
		return ""
	}
	if strings.Contains(runOut("loginctl", "show-user", u, "--property=Linger"), "Linger=no") {
		return ", but lingering is off: it will not run while you are logged out (loginctl enable-linger)"
	}
	return ""
}

func runOut(name string, args ...string) string {
	out, _ := Command(systemBinary(name), args...).Output()
	return string(out)
}
