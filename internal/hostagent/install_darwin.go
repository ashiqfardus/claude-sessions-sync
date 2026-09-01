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
	if everyMinutes <= 0 {
		everyMinutes = 30
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return s, err
	}
	dir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return s, err
	}
	plistPath := filepath.Join(dir, Label+".plist")

	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>` + xmlEscape(Label) + `</string>
  <key>ProgramArguments</key>
  <array>
    <string>` + xmlEscape(binary) + `</string>
    <string>push</string>
    <string>--quiet</string>
  </array>
  <key>StartInterval</key><integer>` + strconv.Itoa(everyMinutes*60) + `</integer>
  <key>RunAtLoad</key><false/>
  <key>ProcessType</key><string>Background</string>
</dict>
</plist>
`
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

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
