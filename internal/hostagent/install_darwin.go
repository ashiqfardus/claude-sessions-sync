package hostagent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func plistPath(j Job) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", j.LaunchdLabel()+".plist"), nil
}

// Install writes a launchd user agent and loads it.
//
// NOT VERIFIED ON A MAC beyond CI. Treat with suspicion.
func Install(j Job, everyMinutes int) (Status, error) {
	s := Status{Mechanism: "launchd"}
	everyMinutes = normalMinutes(everyMinutes)

	path, err := plistPath(j)
	if err != nil {
		return s, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return s, err
	}
	if err := os.WriteFile(path, []byte(LaunchdPlist(j, everyMinutes)), 0o644); err != nil {
		return s, err
	}

	// Reload rather than assume: bootout first so a changed plist takes effect.
	uid := strconv.Itoa(os.Getuid())
	_ = exec.Command(systemBinary("launchctl"), "bootout", "gui/"+uid+"/"+j.LaunchdLabel()).Run()

	// bootout returns before the job is gone, and bootstrapping in that window fails
	// with "Input/output error". Retry briefly, then fall back to the deprecated
	// `load -w` for older systems.
	var out []byte
	for attempt := 0; attempt < 5; attempt++ {
		if out, err = exec.Command(systemBinary("launchctl"), "bootstrap", "gui/"+uid, path).CombinedOutput(); err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		if out2, err2 := exec.Command(systemBinary("launchctl"), "load", "-w", path).CombinedOutput(); err2 != nil {
			return s, fmt.Errorf("launchctl could not load %s: %s / %s",
				path, strings.TrimSpace(string(out)), strings.TrimSpace(string(out2)))
		}
	}

	s.Installed = true
	s.Detail = fmt.Sprintf("%s, every %d minutes", path, everyMinutes)
	return s, nil
}

// Uninstall unloads and removes this profile's launchd agent.
func Uninstall(j Job) error {
	path, err := plistPath(j)
	if err != nil {
		return err
	}

	uid := strconv.Itoa(os.Getuid())
	_ = exec.Command(systemBinary("launchctl"), "bootout", "gui/"+uid+"/"+j.LaunchdLabel()).Run()
	_ = exec.Command(systemBinary("launchctl"), "unload", path).Run()

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
