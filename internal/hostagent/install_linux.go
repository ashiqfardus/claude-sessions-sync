package hostagent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Install registers a systemd user timer, falling back to cron.
//
// NOT VERIFIED ON LINUX beyond CI. See the note in install_darwin.go.
func Install(j Job, everyMinutes int) (Status, error) {
	s := Status{Mechanism: "systemd"}
	everyMinutes = normalMinutes(everyMinutes)

	home, err := os.UserHomeDir()
	if err != nil {
		return s, err
	}
	unitDir := filepath.Join(home, ".config", "systemd", "user")

	if _, lookErr := exec.LookPath("systemctl"); lookErr == nil {
		if err := os.MkdirAll(unitDir, 0o755); err != nil {
			return s, err
		}
		if err := os.WriteFile(filepath.Join(unitDir, j.SystemdServiceName()), []byte(SystemdService(j)), 0o644); err != nil {
			return s, err
		}
		if err := os.WriteFile(filepath.Join(unitDir, j.SystemdTimerName()), []byte(SystemdTimer(everyMinutes)), 0o644); err != nil {
			return s, err
		}

		_ = exec.Command(systemBinary("systemctl"), "--user", "daemon-reload").Run()
		out, err := exec.Command(systemBinary("systemctl"), "--user", "enable", "--now", j.SystemdTimerName()).CombinedOutput()
		if err == nil {
			s.Installed = true
			s.Detail = fmt.Sprintf("%s, every %d minutes", j.SystemdTimerName(), everyMinutes)
			s.Detail += lingerNote()
			return s, nil
		}
		// systemctl on PATH does not mean a user bus exists (WSL, containers). Undo
		// the unit files and fall back to cron rather than failing outright.
		os.Remove(filepath.Join(unitDir, j.SystemdServiceName()))
		os.Remove(filepath.Join(unitDir, j.SystemdTimerName()))
		if _, cronErr := exec.LookPath("crontab"); cronErr != nil {
			return s, fmt.Errorf("systemctl could not enable %s: %s", j.SystemdTimerName(), strings.TrimSpace(string(out)))
		}
	}

	s.Mechanism = "cron"
	if err := rewriteCrontab(j, CronLine(j, everyMinutes)); err != nil {
		return s, err
	}
	s.Installed = true
	s.Detail = fmt.Sprintf("crontab entry, every %d minutes", everyMinutes)
	return s, nil
}

// Uninstall removes this profile's timer and crontab entry.
func Uninstall(j Job) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	unitDir := filepath.Join(home, ".config", "systemd", "user")

	if _, lookErr := exec.LookPath("systemctl"); lookErr == nil {
		_ = exec.Command(systemBinary("systemctl"), "--user", "disable", "--now", j.SystemdTimerName()).Run()
		for _, f := range []string{j.SystemdTimerName(), j.SystemdServiceName()} {
			if err := os.Remove(filepath.Join(unitDir, f)); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		_ = exec.Command(systemBinary("systemctl"), "--user", "daemon-reload").Run()
	}

	// Remove our crontab line too, in case it was installed by the fallback.
	if _, err := exec.LookPath("crontab"); err == nil {
		return rewriteCrontab(j, "")
	}
	return nil
}

// rewriteCrontab drops this job's line, keeps every other line exactly, and appends
// add when it is not empty. Unchanged crontabs are not rewritten.
func rewriteCrontab(j Job, add string) error {
	existing, _ := exec.Command(systemBinary("crontab"), "-l").Output()

	var kept []string
	removed := false
	for _, l := range strings.Split(strings.TrimRight(string(existing), "\n"), "\n") {
		if ownsCronLine(j, l) {
			removed = true
			continue
		}
		if l != "" || len(kept) > 0 {
			kept = append(kept, l)
		}
	}
	if add == "" && !removed {
		return nil
	}
	if add != "" {
		kept = append(kept, add)
	}

	cmd := exec.Command(systemBinary("crontab"), "-")
	cmd.Stdin = strings.NewReader(strings.Join(kept, "\n") + "\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("crontab failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
