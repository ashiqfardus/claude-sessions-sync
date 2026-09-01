package hostagent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Install registers a systemd user timer, falling back to cron.
//
// NOT VERIFIED ON LINUX beyond CI. See the note in install_darwin.go.
func Install(binary string, everyMinutes int) (Status, error) {
	s := Status{Mechanism: "systemd"}
	if everyMinutes <= 0 {
		everyMinutes = 30
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return s, err
	}
	unitDir := filepath.Join(home, ".config", "systemd", "user")

	if _, lookErr := exec.LookPath("systemctl"); lookErr == nil {
		if err := os.MkdirAll(unitDir, 0o755); err != nil {
			return s, err
		}

		service := "[Unit]\nDescription=Archive Claude Code sessions\n\n" +
			"[Service]\nType=oneshot\n" +
			"ExecStart=" + binary + " push --quiet\n"
		timer := "[Unit]\nDescription=Archive Claude Code sessions every " +
			strconv.Itoa(everyMinutes) + " minutes\n\n" +
			"[Timer]\nOnBootSec=5min\nOnUnitActiveSec=" + strconv.Itoa(everyMinutes) + "min\n" +
			"Persistent=true\n\n[Install]\nWantedBy=timers.target\n"

		base := strings.TrimSuffix(Unit, ".timer")
		if err := os.WriteFile(filepath.Join(unitDir, base+".service"), []byte(service), 0o644); err != nil {
			return s, err
		}
		if err := os.WriteFile(filepath.Join(unitDir, Unit), []byte(timer), 0o644); err != nil {
			return s, err
		}

		_ = exec.Command(systemBinary("systemctl"), "--user", "daemon-reload").Run()
		if out, err := exec.Command(systemBinary("systemctl"), "--user", "enable", "--now", Unit).CombinedOutput(); err != nil {
			return s, fmt.Errorf("systemctl could not enable %s: %s", Unit, strings.TrimSpace(string(out)))
		}

		s.Installed = true
		s.Detail = fmt.Sprintf("%s, every %d minutes", Unit, everyMinutes)
		s.Detail += lingerNote()
		return s, nil
	}

	// No systemd: fall back to cron.
	s.Mechanism = "cron"
	line := fmt.Sprintf("*/%d * * * * %s push --quiet\n", everyMinutes, binary)
	existing, _ := exec.Command(systemBinary("crontab"), "-l").Output()

	var kept []string
	for _, l := range strings.Split(string(existing), "\n") {
		if strings.TrimSpace(l) != "" && !strings.Contains(l, "claude-sessions") {
			kept = append(kept, l)
		}
	}
	kept = append(kept, strings.TrimRight(line, "\n"))

	cmd := exec.Command(systemBinary("crontab"), "-")
	cmd.Stdin = strings.NewReader(strings.Join(kept, "\n") + "\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		return s, fmt.Errorf("crontab failed: %s", strings.TrimSpace(string(out)))
	}

	s.Installed = true
	s.Detail = fmt.Sprintf("crontab entry, every %d minutes", everyMinutes)
	return s, nil
}

// Uninstall removes the timer or the crontab entry.
func Uninstall() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	unitDir := filepath.Join(home, ".config", "systemd", "user")
	base := strings.TrimSuffix(Unit, ".timer")

	if _, lookErr := exec.LookPath("systemctl"); lookErr == nil {
		_ = exec.Command(systemBinary("systemctl"), "--user", "disable", "--now", Unit).Run()
		for _, f := range []string{Unit, base + ".service"} {
			if err := os.Remove(filepath.Join(unitDir, f)); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		_ = exec.Command(systemBinary("systemctl"), "--user", "daemon-reload").Run()
	}

	// Remove any crontab line too, in case it was installed by the fallback.
	if existing, err := exec.Command(systemBinary("crontab"), "-l").Output(); err == nil {
		var kept []string
		removed := false
		for _, l := range strings.Split(string(existing), "\n") {
			if strings.Contains(l, "claude-sessions") {
				removed = true
				continue
			}
			if strings.TrimSpace(l) != "" {
				kept = append(kept, l)
			}
		}
		if removed {
			cmd := exec.Command(systemBinary("crontab"), "-")
			cmd.Stdin = strings.NewReader(strings.Join(kept, "\n") + "\n")
			_ = cmd.Run()
		}
	}
	return nil
}
