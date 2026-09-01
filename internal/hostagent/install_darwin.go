package hostagent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Install writes a launchd user agent and loads it.
//
// NOT VERIFIED ON A MAC. This machine is Windows-only; the darwin paths compile and
// run in CI but have never been used in anger. Treat with suspicion.
func Install(binary string, everyMinutes int) (Status, error) {
	s := Status{Mechanism: "launchd"}
	everyMinutes = normalMinutes(everyMinutes)

	home, err := os.UserHomeDir()
	if err != nil {
		return s, err
	}
	dir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return s, err
	}
	plistPath := filepath.Join(dir, Label+".plist")

	plist := LaunchdPlist(Label, binary, everyMinutes)
	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		return s, err
	}

	// Reload rather than assume: bootout first so a changed plist takes effect.
	uid := strconv.Itoa(os.Getuid())
	_ = exec.Command(systemBinary("launchctl"), "bootout", "gui/"+uid+"/"+Label).Run()

	// `bootstrap` is the current verb; `load -w` is deprecated but is the fallback for
	// older systems.
	if out, err := exec.Command(systemBinary("launchctl"), "bootstrap", "gui/"+uid, plistPath).CombinedOutput(); err != nil {
		if out2, err2 := exec.Command(systemBinary("launchctl"), "load", "-w", plistPath).CombinedOutput(); err2 != nil {
			return s, fmt.Errorf("launchctl could not load %s: %s / %s",
				plistPath, strings.TrimSpace(string(out)), strings.TrimSpace(string(out2)))
		}
	}

	s.Installed = true
	s.Detail = fmt.Sprintf("%s, every %d minutes", plistPath, everyMinutes)
	return s, nil
}

// Uninstall unloads and removes the launchd agent.
func Uninstall() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	plistPath := filepath.Join(home, "Library", "LaunchAgents", Label+".plist")

	uid := strconv.Itoa(os.Getuid())
	_ = exec.Command(systemBinary("launchctl"), "bootout", "gui/"+uid+"/"+Label).Run()
	_ = exec.Command(systemBinary("launchctl"), "unload", plistPath).Run()

	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
